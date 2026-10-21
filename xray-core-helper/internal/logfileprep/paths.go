package logfileprep

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
)

// ErrPathAccess reports an ancestor that the Xray service identity cannot traverse.
var ErrPathAccess = errors.New("Xray cannot traverse the configured log directory")

// ErrInvalidPath reports a configured log destination that is not a clean,
// absolute, non-empty file path. Callers select the managed defaults instead of
// passing "none" or an empty value here.
var ErrInvalidPath = errors.New("log preparation received an invalid configured path")

// PathPreparer prepares the file-backed access and error destinations supplied
// through trusted helper configuration. The managed defaults continue to use
// Preparer's fixed /var/log/xray behavior; arbitrary destinations only touch a
// configured directory chain. Existing parents keep their metadata.
type PathPreparer struct {
	identity   IdentityResolver
	files      FileSystem
	accessPath string
	errorPath  string
}

// NewWithPaths validates the configured destinations and creates a preparer.
// It does not resolve the service identity or touch the filesystem.
func NewWithPaths(identity IdentityResolver, files FileSystem, accessPath, errorPath string) (*PathPreparer, error) {
	if identity == nil {
		return nil, ErrIdentityUnavailable
	}
	if !validConfiguredPath(accessPath) || !validConfiguredPath(errorPath) {
		return nil, ErrInvalidPath
	}
	if files == nil {
		files = osFileSystem{}
	}
	return &PathPreparer{
		identity:   identity,
		files:      files,
		accessPath: accessPath,
		errorPath:  errorPath,
	}, nil
}

func validConfiguredPath(value string) bool {
	return value != "" && value != "none" && bootstrap.ValidLogDestination(value)
}

type configuredSelection struct {
	standardAccess bool
	standardError  bool
	custom         []string
}

// Prepare creates or corrects only file-backed destinations selected by the
// request. Managed access/error paths are delegated to Preparer; a custom path
// is prepared independently. Disabled and journal selections perform no
// filesystem or identity work.
func (preparer *PathPreparer) Prepare(ctx context.Context, request api.LogConfigurationRequest) (Snapshot, error) {
	selection, err := preparer.selectTargets(request)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, ErrPreparation
	}
	if !selection.standardAccess && !selection.standardError && len(selection.custom) == 0 {
		return noopPreparation{}, nil
	}
	if ctx.Err() != nil {
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

	combined := &combinedPreparation{}
	fail := func(cause error) (Snapshot, error) {
		rollbackErr := combined.Rollback()
		if rollbackErr != nil || errors.Is(cause, ErrRollbackFailed) {
			return nil, errors.Join(ErrPreparation, ErrRollbackFailed)
		}
		if errors.Is(cause, ErrPathAccess) {
			return nil, ErrPathAccess
		}
		if errors.Is(cause, ErrUnsafeFilesystem) {
			return nil, ErrUnsafeFilesystem
		}
		return nil, ErrPreparation
	}

	if selection.standardAccess || selection.standardError {
		snapshot, prepareErr := preparer.prepareStandard(ctx, identity, selection.standardAccess, selection.standardError)
		if snapshot != nil {
			combined.add(snapshot)
		}
		if prepareErr != nil {
			return fail(prepareErr)
		}
	}

	for _, customPath := range selection.custom {
		if ctx.Err() != nil {
			return fail(ErrPreparation)
		}
		snapshot, prepareErr := prepareCustomPath(preparer.files, identity, customPath)
		if snapshot != nil {
			combined.add(snapshot)
		}
		if prepareErr != nil {
			return fail(prepareErr)
		}
	}
	return combined, nil
}

func (preparer *PathPreparer) selectTargets(request api.LogConfigurationRequest) (configuredSelection, error) {
	var selection configuredSelection
	targets := []struct {
		mode api.LogTargetMode
		path string
	}{
		{mode: request.Access.Mode, path: preparer.accessPath},
		{mode: request.Error.Mode, path: preparer.errorPath},
	}

	seenCustom := make(map[string]struct{})
	for _, target := range targets {
		switch target.mode {
		case api.LogTargetDisabled, api.LogTargetJournal:
		case api.LogTargetFile:
			switch target.path {
			case AccessLogPath:
				selection.standardAccess = true
			case ErrorLogPath:
				selection.standardError = true
			default:
				if _, seen := seenCustom[target.path]; !seen {
					seenCustom[target.path] = struct{}{}
					selection.custom = append(selection.custom, target.path)
				}
			}
		default:
			return configuredSelection{}, ErrInvalidSelection
		}
	}
	return selection, nil
}

func (preparer *PathPreparer) prepareStandard(ctx context.Context, identity Identity, access, errorLog bool) (Snapshot, error) {
	request := api.LogConfigurationRequest{
		Access: api.LogTarget{Mode: api.LogTargetJournal},
		Error:  api.LogTarget{Mode: api.LogTargetJournal},
	}
	if access {
		request.Access.Mode = api.LogTargetFile
	}
	if errorLog {
		request.Error.Mode = api.LogTargetFile
	}

	standard, err := New(IdentityResolverFunc(func(context.Context) (Identity, error) {
		return identity, nil
	}), preparer.files)
	if err != nil {
		return nil, err
	}
	return standard.Prepare(ctx, request)
}

type pathNode struct {
	name      string
	directory Directory
	info      os.FileInfo
	created   bool
}

type pathPreparation struct {
	root     Directory
	rootInfo os.FileInfo
	nodes    []pathNode
	base     string
	entry    *preparedFile
	done     bool
}

// prepareCustomPath traverses parents with no-follow descriptors, creates only
// missing directories and preserves metadata of every existing ancestor.
func prepareCustomPath(files FileSystem, identity Identity, value string) (*pathPreparation, error) {
	root, err := files.OpenRoot()
	state := &pathPreparation{root: root}
	if err != nil || root == nil {
		return state, ErrPreparation
	}
	state.rootInfo, err = root.Info()
	if err != nil {
		return state, ErrPreparation
	}
	if !state.rootInfo.IsDir() {
		return state, ErrUnsafeFilesystem
	}

	components := strings.Split(strings.TrimPrefix(value, "/"), "/")
	if len(components) == 0 {
		return state, ErrInvalidPath
	}
	state.base = components[len(components)-1]
	current := root
	if !directoryTraversable(state.rootInfo, identity) {
		return state, ErrPathAccess
	}
	for _, name := range components[:len(components)-1] {
		if !state.chainMatches() {
			return state, ErrUnsafeFilesystem
		}
		directory, openErr := current.OpenDirectory(name)
		created := false
		if errors.Is(openErr, os.ErrNotExist) {
			if err := current.Mkdir(name, createDirMode); err != nil {
				return state, ErrPreparation
			}
			created = true
			directory, openErr = current.OpenDirectory(name)
		}
		if openErr != nil {
			if unsafeDirectoryError(openErr) {
				return state, ErrUnsafeFilesystem
			}
			return state, ErrPreparation
		}
		info, infoErr := directory.Info()
		if infoErr != nil || !info.IsDir() {
			_ = directory.Close()
			return state, ErrUnsafeFilesystem
		}
		state.nodes = append(state.nodes, pathNode{name: name, directory: directory, info: info, created: created})
		if created {
			if !state.chainMatches() {
				return state, ErrUnsafeFilesystem
			}
			if err := directory.Chown(identity.UID, identity.GID); err != nil {
				return state, ErrPreparation
			}
			if err := directory.Chmod(directoryMode); err != nil {
				return state, ErrPreparation
			}
			if err := directory.Sync(); err != nil {
				return state, ErrPreparation
			}
			if err := current.Sync(); err != nil {
				return state, ErrPreparation
			}
			info, infoErr = directory.Info()
			if infoErr != nil || !metadataMatches(directory, identity, directoryMode) {
				return state, ErrPreparation
			}
		}
		if !directoryTraversable(info, identity) {
			return state, ErrPathAccess
		}
		current = directory
	}

	if !state.chainMatches() {
		return state, ErrUnsafeFilesystem
	}
	parent := state.parent()
	if parent == nil {
		return state, ErrUnsafeFilesystem
	}

	entry, err := prepareFile(parent, state.base)
	if entry != nil {
		state.entry = entry
	}
	if err != nil {
		return state, err
	}
	if !state.chainMatches() {
		return state, ErrUnsafeFilesystem
	}
	if !sameFile(parent, entry.name, entry.info) {
		return state, ErrUnsafeFilesystem
	}

	if err := entry.prepare(parent, identity); err != nil {
		return state, err
	}
	if err := parent.Sync(); err != nil {
		return state, ErrPreparation
	}
	if !state.chainMatches() {
		return state, ErrUnsafeFilesystem
	}
	if !sameFile(parent, entry.name, entry.info) {
		return state, ErrUnsafeFilesystem
	}
	if !fileMetadataMatches(entry.file, identity, fileMode) {
		return state, ErrPreparation
	}
	return state, nil
}

func (state *pathPreparation) parent() Directory {
	if len(state.nodes) == 0 {
		return state.root
	}
	return state.nodes[len(state.nodes)-1].directory
}

// chainMatches reopens the configured chain from the held root descriptor and
// verifies that every component still names the inode recorded during
// traversal. It is called before every mutation, commit, and rollback.
func (state *pathPreparation) chainMatches() bool {
	if state.root == nil || state.rootInfo == nil {
		return false
	}
	info, err := state.root.Info()
	if err != nil || !info.IsDir() || !os.SameFile(state.rootInfo, info) {
		return false
	}

	opened := make([]Directory, 0, len(state.nodes))
	defer func() {
		for index := len(opened) - 1; index >= 0; index-- {
			_ = opened[index].Close()
		}
	}()

	current := state.root
	for _, node := range state.nodes {
		directory, openErr := current.OpenDirectory(node.name)
		if openErr != nil {
			return false
		}
		opened = append(opened, directory)
		actual, infoErr := directory.Info()
		if infoErr != nil || !actual.IsDir() || node.info == nil || !os.SameFile(node.info, actual) {
			return false
		}
		current = directory
	}
	for _, node := range state.nodes {
		actual, infoErr := node.directory.Info()
		if infoErr != nil || node.info == nil || !os.SameFile(node.info, actual) {
			return false
		}
	}
	return true
}

func (state *pathPreparation) Rollback() error {
	if state.done {
		return nil
	}
	var failures []error
	if state.entry != nil && !state.entry.rolledBack {
		if !state.chainMatches() {
			failures = append(failures, ErrUnsafeFilesystem)
		} else {
			parent := state.parent()
			switch {
			case state.entry.created:
				if parent == nil || state.entry.info == nil ||
					!sameFile(parent, state.entry.name, state.entry.info) {
					failures = append(failures, ErrUnsafeFilesystem)
				} else if err := parent.Remove(state.entry.name); err != nil {
					failures = append(failures, ErrRollbackFailed)
				} else {
					state.entry.rolledBack = true
				}
			case state.entry.previousValid:
				if parent == nil ||
					!sameFile(parent, state.entry.name, state.entry.info) {
					failures = append(failures, ErrUnsafeFilesystem)
				} else if err := restoreMetadata(state.entry.file, state.entry.previous); err != nil {
					failures = append(failures, ErrRollbackFailed)
				} else if err := state.entry.file.Sync(); err != nil {
					failures = append(failures, ErrRollbackFailed)
				} else {
					state.entry.rolledBack = true
				}
			default:
				state.entry.rolledBack = true
			}
		}
	}
	if len(failures) == 0 && state.entry != nil && state.parent() != nil {
		if err := state.parent().Sync(); err != nil {
			failures = append(failures, ErrRollbackFailed)
		}
	}
	if len(failures) == 0 {
		for i := len(state.nodes) - 1; i >= 0; i-- {
			node := &state.nodes[i]
			if !node.created {
				continue
			}
			parent := state.root
			if i > 0 {
				parent = state.nodes[i-1].directory
			}
			if !sameDirectory(parent, node.name, node.info) {
				failures = append(failures, ErrUnsafeFilesystem)
				break
			}
			if err := parent.RemoveDirectory(node.name); err != nil {
				failures = append(failures, ErrRollbackFailed)
				break
			}
			node.created = false
			if err := parent.Sync(); err != nil {
				failures = append(failures, ErrRollbackFailed)
				break
			}
		}
	}
	if err := state.close(); err != nil {
		failures = append(failures, err)
	}
	if len(failures) != 0 {
		return errors.Join(append([]error{ErrRollbackFailed}, failures...)...)
	}
	state.done = true
	return nil
}

func (state *pathPreparation) Commit() error {
	if state.done {
		return nil
	}
	if !state.chainMatches() {
		state.done = true
		return errors.Join(ErrRollbackFailed, state.close())
	}
	if state.entry != nil {
		parent := state.parent()
		if parent == nil || !sameFile(parent, state.entry.name, state.entry.info) {
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

func (state *pathPreparation) close() error {
	var failures []error
	if state.entry != nil && state.entry.file != nil {
		if err := state.entry.file.Close(); err != nil {
			failures = append(failures, err)
		}
		state.entry.file = nil
	}
	for index := len(state.nodes) - 1; index >= 0; index-- {
		if state.nodes[index].directory != nil {
			if err := state.nodes[index].directory.Close(); err != nil {
				failures = append(failures, err)
			}
			state.nodes[index].directory = nil
		}
	}
	if state.root != nil {
		if err := state.root.Close(); err != nil {
			failures = append(failures, err)
		}
		state.root = nil
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	return nil
}

func unsafeDirectoryError(err error) bool {
	return errors.Is(err, syscall.ELOOP) ||
		errors.Is(err, syscall.ENOTDIR) ||
		errors.Is(err, syscall.EISDIR)
}

type combinedPreparation struct {
	snapshots []Snapshot
	done      bool
}

func (preparation *combinedPreparation) add(snapshot Snapshot) {
	if snapshot != nil {
		preparation.snapshots = append(preparation.snapshots, snapshot)
	}
}

func (preparation *combinedPreparation) Rollback() error {
	if preparation.done {
		return nil
	}
	var failures []error
	for index := len(preparation.snapshots) - 1; index >= 0; index-- {
		if err := preparation.snapshots[index].Rollback(); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) != 0 {
		return errors.Join(append([]error{ErrRollbackFailed}, failures...)...)
	}
	preparation.done = true
	return nil
}

func (preparation *combinedPreparation) Commit() error {
	if preparation.done {
		return nil
	}
	var failures []error
	for _, snapshot := range preparation.snapshots {
		if err := snapshot.Commit(); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	preparation.done = true
	return nil
}

// Existing ancestors are never chmod/chowned. A private ancestor must be made
// traversable by its administrator; changing it here would grant unrelated data.
func directoryTraversable(info os.FileInfo, identity Identity) bool {
	if info == nil || !info.IsDir() {
		return false
	}
	metadata, err := readMetadata(info)
	if err != nil {
		return false
	}
	mode := metadata.mode.Perm()
	switch {
	case metadata.uid == identity.UID:
		return mode&0100 != 0
	case metadata.gid == identity.GID:
		return mode&0010 != 0
	default:
		return mode&0001 != 0
	}
}
