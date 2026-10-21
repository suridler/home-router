package logfileprep

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"home-router/xray-core-helper/internal/api"
)

const (
	ManagedDirectory = "/var/log/xray"
	AccessLogPath    = "/var/log/xray/access.log"
	ErrorLogPath     = "/var/log/xray/error.log"

	directoryMode  = os.FileMode(0750)
	fileMode       = os.FileMode(0640)
	createDirMode  = os.FileMode(0700)
	createFileMode = os.FileMode(0600)
)

var (
	ErrInvalidIdentity     = errors.New("log preparation requires a valid non-root Xray service identity")
	ErrIdentityUnavailable = errors.New("Xray service identity is unavailable")
	ErrInvalidSelection    = errors.New("log preparation received an unsupported target mode")
	ErrUnsafeFilesystem    = errors.New("log preparation rejected an unsafe filesystem entry")
	ErrPreparation         = errors.New("log-file preparation failed")
	ErrRollbackFailed      = errors.New("log-file preparation rollback failed")
)

type Identity struct {
	UID int
	GID int
}

type IdentityResolver interface {
	ResolveXrayIdentity(context.Context) (Identity, error)
}

type IdentityResolverFunc func(context.Context) (Identity, error)

func (resolve IdentityResolverFunc) ResolveXrayIdentity(ctx context.Context) (Identity, error) {
	return resolve(ctx)
}

// Snapshot is an in-process undo record. Rollback restores metadata changed
// by Prepare and removes only entries created by that invocation. Commit keeps
// the prepared paths and releases the held directory and file descriptors.
type Snapshot interface {
	Rollback() error
	Commit() error
}

type Preparer struct {
	identity IdentityResolver
	files    FileSystem
}

type metadata struct {
	mode os.FileMode
	uid  int
	gid  int
}

type fileTarget struct {
	name string
}

type preparedFile struct {
	name          string
	file          File
	info          os.FileInfo
	previous      metadata
	previousValid bool
	created       bool
	rolledBack    bool
}

type preparation struct {
	root         Directory
	rootInfo     os.FileInfo
	varDirectory Directory
	varInfo      os.FileInfo
	parent       Directory
	parentInfo   os.FileInfo
	logDirectory Directory
	dirInfo      os.FileInfo
	dirPrevious  metadata
	dirCreated   bool
	dirChanged   bool
	files        []*preparedFile
	done         bool
}

type noopPreparation struct{}

func (noopPreparation) Rollback() error { return nil }
func (noopPreparation) Commit() error   { return nil }

func New(identity IdentityResolver, files FileSystem) (*Preparer, error) {
	if identity == nil {
		return nil, ErrIdentityUnavailable
	}
	if files == nil {
		files = osFileSystem{}
	}
	return &Preparer{identity: identity, files: files}, nil
}

// Prepare creates or corrects only the fixed file-backed access/error targets.
// Journal and disabled targets are no-ops; no file contents are read or written.
func (preparer *Preparer) Prepare(ctx context.Context, request api.LogConfigurationRequest) (Snapshot, error) {
	targets, err := selectedFiles(request)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return noopPreparation{}, nil
	}
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrPreparation
	}

	identity, err := preparer.identity.ResolveXrayIdentity(ctx)
	if err != nil {
		return nil, ErrIdentityUnavailable
	}
	if !validIdentity(identity) {
		return nil, ErrInvalidIdentity
	}
	if ctx.Err() != nil {
		return nil, ErrPreparation
	}

	root, err := preparer.files.OpenRoot()
	if err != nil {
		return nil, ErrPreparation
	}
	state := &preparation{root: root}
	fail := func(cause error) (Snapshot, error) {
		rollbackErr := state.rollback()
		closeErr := state.close()
		if rollbackErr != nil || closeErr != nil {
			return nil, errors.Join(ErrPreparation, ErrRollbackFailed)
		}
		if errors.Is(cause, ErrUnsafeFilesystem) {
			return nil, ErrUnsafeFilesystem
		}
		return nil, ErrPreparation
	}

	var varDirectory Directory
	var logParent Directory
	varDirectory, err = root.OpenDirectory("var")
	if err != nil {
		_ = state.close()
		return nil, ErrUnsafeFilesystem
	}
	state.varDirectory = varDirectory
	state.rootInfo, err = root.Info()
	if err != nil {
		_ = state.close()
		return nil, ErrPreparation
	}
	state.varInfo, err = varDirectory.Info()
	if err != nil || !state.varInfo.IsDir() {
		_ = state.close()
		return nil, ErrUnsafeFilesystem
	}
	logParent, err = varDirectory.OpenDirectory("log")
	if err != nil {
		_ = state.close()
		return nil, ErrUnsafeFilesystem
	}
	state.parent = logParent
	state.parentInfo, err = logParent.Info()
	if err != nil || !state.parentInfo.IsDir() {
		return fail(ErrUnsafeFilesystem)
	}

	logDirectory, openErr := logParent.OpenDirectory("xray")
	if openErr != nil {
		if !errors.Is(openErr, os.ErrNotExist) {
			return fail(ErrUnsafeFilesystem)
		}
		if mkdirErr := logParent.Mkdir("xray", createDirMode); mkdirErr != nil {
			if errors.Is(mkdirErr, syscall.EEXIST) {
				return fail(ErrUnsafeFilesystem)
			}
			return fail(ErrPreparation)
		}
		state.dirCreated = true
		logDirectory, openErr = logParent.OpenDirectory("xray")
		if openErr != nil {
			return fail(ErrUnsafeFilesystem)
		}
	}
	state.logDirectory = logDirectory
	state.dirInfo, err = logDirectory.Info()
	if err != nil || !state.dirInfo.IsDir() {
		return fail(ErrUnsafeFilesystem)
	}
	state.dirPrevious, err = readMetadata(state.dirInfo)
	if err != nil {
		return fail(ErrPreparation)
	}
	if !state.dirCreated {
		state.dirChanged = true
	}
	if err := logDirectory.Chown(identity.UID, identity.GID); err != nil {
		return fail(ErrPreparation)
	}
	if err := logDirectory.Chmod(directoryMode); err != nil {
		return fail(ErrPreparation)
	}
	if !sameDirectory(logParent, "xray", state.dirInfo) {
		return fail(ErrUnsafeFilesystem)
	}
	if !state.pathsMatch() {
		return fail(ErrUnsafeFilesystem)
	}
	if !metadataMatches(logDirectory, identity, directoryMode) {
		return fail(ErrPreparation)
	}

	for _, target := range targets {
		if ctx.Err() != nil {
			return fail(ErrPreparation)
		}
		entry, err := prepareFile(logDirectory, target.name)
		if entry != nil {
			state.files = append(state.files, entry)
		}
		if err != nil {
			return fail(err)
		}
		if err := entry.prepare(logDirectory, identity); err != nil {
			return fail(err)
		}
	}
	if err := logDirectory.Sync(); err != nil {
		return fail(ErrPreparation)
	}
	if err := logParent.Sync(); err != nil {
		return fail(ErrPreparation)
	}
	if !sameDirectory(logParent, "xray", state.dirInfo) {
		return fail(ErrUnsafeFilesystem)
	}
	if !state.pathsMatch() {
		return fail(ErrUnsafeFilesystem)
	}
	for _, entry := range state.files {
		if !sameFile(logDirectory, entry.name, entry.info) {
			return fail(ErrUnsafeFilesystem)
		}
		if !fileMetadataMatches(entry.file, identity, fileMode) {
			return fail(ErrPreparation)
		}
	}
	return state, nil
}

func selectedFiles(request api.LogConfigurationRequest) ([]fileTarget, error) {
	targets := []struct {
		value api.LogTarget
		name  string
	}{
		{value: request.Access, name: "access.log"},
		{value: request.Error, name: "error.log"},
	}
	selected := make([]fileTarget, 0, len(targets))
	for _, target := range targets {
		switch target.value.Mode {
		case api.LogTargetDisabled, api.LogTargetJournal:
		case api.LogTargetFile:
			selected = append(selected, fileTarget{name: target.name})
		default:
			return nil, ErrInvalidSelection
		}
	}
	return selected, nil
}

func validIdentity(identity Identity) bool {
	return identity.UID > 0 && identity.GID > 0 &&
		uint64(identity.UID) <= uint64(^uint32(0)) &&
		uint64(identity.GID) <= uint64(^uint32(0))
}

func prepareFile(directory Directory, name string) (*preparedFile, error) {
	file, err := directory.OpenFile(name, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		file, err = directory.OpenFile(name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NONBLOCK, createFileMode)
		if errors.Is(err, syscall.EEXIST) {
			file, err = directory.OpenFile(name, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
		} else if err == nil {
			created = true
		}
	}
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EISDIR) || errors.Is(err, syscall.ENOTDIR) {
			return nil, ErrUnsafeFilesystem
		}
		return nil, ErrPreparation
	}
	info, err := file.Info()
	entry := &preparedFile{name: name, file: file, info: info, created: created}
	if err != nil {
		return entry, ErrPreparation
	}
	if !info.Mode().IsRegular() {
		return entry, ErrUnsafeFilesystem
	}
	if !created {
		entry.previous, err = readMetadata(info)
		if err != nil {
			return entry, ErrPreparation
		}
		entry.previousValid = true
	}
	return entry, nil
}

// prepare applies the same file policy for standard and configured destinations.
// Parent traversal and directory metadata remain with their respective owners.
func (entry *preparedFile) prepare(directory Directory, identity Identity) error {
	if err := entry.file.Chown(identity.UID, identity.GID); err != nil {
		return ErrPreparation
	}
	if err := entry.file.Chmod(fileMode); err != nil {
		return ErrPreparation
	}
	if !sameFile(directory, entry.name, entry.info) {
		return ErrUnsafeFilesystem
	}
	if !fileMetadataMatches(entry.file, identity, fileMode) {
		return ErrPreparation
	}
	if err := entry.file.Sync(); err != nil {
		return ErrPreparation
	}
	return nil
}

func (state *preparation) Rollback() error {
	if state.done {
		return nil
	}
	if err := state.rollback(); err != nil {
		return err
	}
	return state.close()
}

func (state *preparation) rollback() error {
	if !state.pathsMatch() {
		return errors.Join(ErrRollbackFailed, ErrUnsafeFilesystem)
	}
	var failures []error
	for index := len(state.files) - 1; index >= 0; index-- {
		entry := state.files[index]
		if entry.rolledBack {
			continue
		}
		if entry.created {
			if entry.info == nil || !sameFile(state.logDirectory, entry.name, entry.info) {
				failures = append(failures, ErrUnsafeFilesystem)
				continue
			}
			if err := state.logDirectory.Remove(entry.name); err != nil {
				failures = append(failures, ErrRollbackFailed)
				continue
			}
			entry.rolledBack = true
			continue
		}
		if !entry.previousValid {
			entry.rolledBack = true
			continue
		}
		if !sameFile(state.logDirectory, entry.name, entry.info) {
			failures = append(failures, ErrUnsafeFilesystem)
			continue
		}
		if err := restoreMetadata(entry.file, entry.previous); err != nil {
			failures = append(failures, ErrRollbackFailed)
			continue
		}
		if err := entry.file.Sync(); err != nil {
			failures = append(failures, ErrRollbackFailed)
			continue
		}
		entry.rolledBack = true
	}

	filesComplete := true
	for _, entry := range state.files {
		if !entry.rolledBack {
			filesComplete = false
			break
		}
	}
	if state.dirCreated {
		if filesComplete {
			if !sameDirectory(state.parent, "xray", state.dirInfo) {
				failures = append(failures, ErrUnsafeFilesystem)
			} else if err := state.parent.RemoveDirectory("xray"); err != nil {
				failures = append(failures, ErrRollbackFailed)
			} else {
				state.dirCreated = false
			}
		}
	} else if state.dirChanged {
		if !sameDirectory(state.parent, "xray", state.dirInfo) {
			failures = append(failures, ErrUnsafeFilesystem)
		} else if err := restoreMetadata(state.logDirectory, state.dirPrevious); err != nil {
			failures = append(failures, ErrRollbackFailed)
		} else {
			state.dirChanged = false
		}
	}
	if len(failures) != 0 {
		return errors.Join(append([]error{ErrRollbackFailed}, failures...)...)
	}
	if state.logDirectory != nil {
		if err := state.logDirectory.Sync(); err != nil {
			return ErrRollbackFailed
		}
	}
	if state.parent != nil {
		if err := state.parent.Sync(); err != nil {
			return ErrRollbackFailed
		}
	}
	state.done = true
	return nil
}

func (state *preparation) Commit() error {
	if state.done {
		return nil
	}
	if !state.pathsMatch() {
		state.done = true
		return errors.Join(ErrRollbackFailed, state.close())
	}
	for _, entry := range state.files {
		if !sameFile(state.logDirectory, entry.name, entry.info) {
			state.done = true
			return errors.Join(ErrRollbackFailed, state.close())
		}
	}
	if err := state.close(); err != nil {
		return errors.Join(ErrPreparation, ErrRollbackFailed)
	}
	state.done = true
	return nil
}

func (state *preparation) close() error {
	var failures []error
	for _, entry := range state.files {
		if entry.file != nil {
			if err := entry.file.Close(); err != nil {
				failures = append(failures, err)
			}
			entry.file = nil
		}
	}
	if state.logDirectory != nil {
		if err := state.logDirectory.Close(); err != nil {
			failures = append(failures, err)
		}
		state.logDirectory = nil
	}
	if state.parent != nil {
		if err := state.parent.Close(); err != nil {
			failures = append(failures, err)
		}
		state.parent = nil
	}
	if state.varDirectory != nil {
		if err := state.varDirectory.Close(); err != nil {
			failures = append(failures, err)
		}
		state.varDirectory = nil
	}
	if state.root != nil {
		if err := state.root.Close(); err != nil {
			failures = append(failures, err)
		}
		state.root = nil
	}
	if len(failures) != 0 {
		return ErrPreparation
	}
	return nil
}

func (state *preparation) pathsMatch() bool {
	if state.root == nil || state.varDirectory == nil || state.parent == nil ||
		state.rootInfo == nil || state.varInfo == nil || state.parentInfo == nil {
		return false
	}
	if !sameDirectory(state.root, "var", state.varInfo) ||
		!sameDirectory(state.varDirectory, "log", state.parentInfo) {
		return false
	}
	return state.dirInfo == nil || sameDirectory(state.parent, "xray", state.dirInfo)
}

func restoreMetadata(file interface {
	Chown(int, int) error
	Chmod(os.FileMode) error
}, previous metadata) error {
	if err := file.Chown(previous.uid, previous.gid); err != nil {
		return err
	}
	if err := file.Chmod(previous.mode); err != nil {
		return err
	}
	return nil
}

func readMetadata(info os.FileInfo) (metadata, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return metadata{}, fmt.Errorf("ownership metadata unavailable")
	}
	return metadata{
		mode: info.Mode().Perm() | info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky),
		uid:  int(stat.Uid),
		gid:  int(stat.Gid),
	}, nil
}

func metadataMatches(file interface {
	Info() (os.FileInfo, error)
}, identity Identity, expected os.FileMode) bool {
	info, err := file.Info()
	if err != nil || info.Mode().Perm() != expected.Perm() ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return false
	}
	actual, err := readMetadata(info)
	return err == nil && actual.uid == identity.UID && actual.gid == identity.GID
}

func fileMetadataMatches(file File, identity Identity, expected os.FileMode) bool {
	return metadataMatches(file, identity, expected)
}

func sameFile(directory Directory, name string, expected os.FileInfo) bool {
	file, err := directory.OpenFile(name, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	actual, statErr := file.Info()
	closeErr := file.Close()
	return statErr == nil && closeErr == nil && expected != nil &&
		os.SameFile(expected, actual) && actual.Mode().IsRegular()
}

func sameDirectory(parent Directory, name string, expected os.FileInfo) bool {
	directory, err := parent.OpenDirectory(name)
	if err != nil {
		return false
	}
	actual, statErr := directory.Info()
	closeErr := directory.Close()
	return statErr == nil && closeErr == nil && expected != nil &&
		os.SameFile(expected, actual) && actual.IsDir()
}
