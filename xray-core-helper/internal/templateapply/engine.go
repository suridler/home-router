package templateapply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/filemetadata"
	"home-router/xray-core-helper/internal/nativewrite"
	"home-router/xray-core-helper/internal/templatebackup"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
)

const maxApplyBytes = 16 << 20

type CrossCheck interface {
	Check(context.Context, templatecatalog.ID, string) error
}

type BackupStore interface {
	Snapshot(context.Context, templatecatalog.ID, templatebackup.Side, templatebackup.State) (templatebackup.Record, error)
	SetState(context.Context, templatecatalog.ID, string, templatebackup.State) (templatebackup.Record, error)
	CompletedGeneration(context.Context, templatecatalog.ID) (uint64, error)
	Restore(context.Context, templatecatalog.ID, string, uint64) (templatebackup.Record, error)
	RestorePending(context.Context, templatecatalog.ID, string) (templatebackup.Record, error)
}

type Runtime interface {
	Prepare(context.Context, templatecatalog.ID) (RuntimeTransaction, error)
}

type RuntimeTransaction interface {
	Apply(context.Context, string) error
	Healthy(context.Context) error
	Rollback(context.Context, string) error
}

type Replacement interface {
	Hash() string
	Commit() error
	Rollback() error
	Close() error
}

type Replacer interface {
	Prepare(source, target string) (Replacement, error)
}

type Result struct {
	TemplateID string
	BackupID   string
	Generation uint64
}

type Engine struct {
	network        bootstrap.NetworkConfig
	renderEnabled  bool
	mu             sync.Mutex
	userGeosite    string
	xrayValues     *xrayconfig.TemplateValues
	loggingValues  func() bootstrap.LoggingConfig
	catalog        templatecatalog.Catalog
	check          CrossCheck
	backups        BackupStore
	runtime        Runtime
	replacer       Replacer
	hash           func(string) (string, error)
	buildCandidate func(context.Context, templatecatalog.Entry, bool) (candidate, error)
	groupPaths     map[templatecatalog.ID]string // test-only fixed-path substitution
}

func NewEngine(check CrossCheck, backups BackupStore, runtime Runtime) (*Engine, error) {
	return NewEngineWithCatalog(check, backups, runtime, templatecatalog.Default())
}

func NewEngineWithCatalog(check CrossCheck, backups BackupStore, runtime Runtime, catalog templatecatalog.Catalog, userGeosite ...string) (*Engine, error) {
	if check == nil || backups == nil || runtime == nil {
		return nil, &Error{Code: CodeRuntime}
	}
	name := bootstrap.DefaultUserGeositeName
	if len(userGeosite) > 0 {
		name = userGeosite[0]
	}
	if !bootstrap.ValidUserGeositeName(name) {
		return nil, &Error{Code: CodeBindingInvalid}
	}
	return &Engine{userGeosite: name, catalog: catalog, check: check, backups: backups, runtime: runtime, replacer: osReplacer{catalog: catalog}, hash: nativeHash}, nil
}

// Apply is intentionally an internal primitive. Phase F will wrap it in a
// durable confirmation transaction before any transport can invoke it.
func (engine *Engine) Apply(ctx context.Context, id templatecatalog.ID, expectedGeneration uint64) (result Result, resultErr error) {
	if systemMember(id) {
		return Result{}, &Error{Code: CodeSafeApply}
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	entry, err := engine.catalog.Lookup(id)
	if err != nil {
		return Result{}, &Error{Code: CodeUnknownTemplate}
	}
	generation, err := engine.backups.CompletedGeneration(ctx, id)
	if err != nil {
		return Result{}, &Error{Code: CodeBackup}
	}
	if generation != expectedGeneration {
		return Result{}, &Error{Code: CodeGeneration}
	}
	candidate, err := engine.candidate(ctx, entry, true)
	if err != nil {
		return Result{}, err
	}
	defer candidate.close()
	validatedHash := candidate.hash
	if validatedHash == "" {
		return Result{}, &Error{Code: CodeValidation}
	}
	if err := engine.check.Check(ctx, id, candidate.path); err != nil {
		return Result{}, err
	}
	currentHash, err := engine.hash(candidate.path)
	if err != nil || currentHash != validatedHash {
		return Result{}, &Error{Code: CodeValidation}
	}
	runtime, err := engine.runtime.Prepare(ctx, id)
	if err != nil {
		return Result{}, &Error{Code: CodeRuntime}
	}
	backup, err := engine.backups.Snapshot(ctx, id, templatebackup.SideSystem, templatebackup.StatePending)
	if err != nil {
		return Result{}, &Error{Code: CodeBackup}
	}
	replacement, err := engine.replacer.Prepare(candidate.path, entry.SystemPath)
	if err != nil {
		engine.mark(ctx, id, backup.BackupID, templatebackup.StateRollback)
		return Result{}, &Error{Code: CodeReplace}
	}
	if replacement.Hash() != validatedHash {
		_ = replacement.Close()
		engine.mark(ctx, id, backup.BackupID, templatebackup.StateRollback)
		return Result{}, &Error{Code: CodeValidation}
	}
	defer replacement.Close()
	committed := false
	runtimeStarted := false
	defer func() {
		if committed || resultErr == nil {
			return
		}
		fileErr := replacement.Rollback()
		var runtimeErr error
		if runtimeStarted {
			runtimeErr = runtime.Rollback(context.WithoutCancel(ctx), entry.SystemPath)
		}
		state := templatebackup.StateRollback
		if fileErr != nil || runtimeErr != nil {
			state = templatebackup.StateRecoveryRequired
			resultErr = &Error{Code: CodeRollback}
		}
		engine.mark(context.WithoutCancel(ctx), id, backup.BackupID, state)
	}()
	if err := replacement.Commit(); err != nil {
		return Result{}, &Error{Code: CodeReplace}
	}
	runtimeStarted = true
	if err := runtime.Apply(ctx, entry.SystemPath); err != nil {
		return Result{}, &Error{Code: CodeRuntime}
	}
	if err := runtime.Healthy(ctx); err != nil {
		return Result{}, effectError(err, CodeHealth)
	}
	completed, err := engine.backups.SetState(ctx, id, backup.BackupID, templatebackup.StateCompleted)
	if err != nil {
		return Result{}, &Error{Code: CodeBackup}
	}
	committed = true
	return Result{TemplateID: string(id), BackupID: backup.BackupID, Generation: completed.Generation}, nil
}

func effectError(err error, fallback Code) error {
	var failure *Error
	if errors.As(err, &failure) {
		switch failure.Code {
		case CodeXrayEffect:
			return failure
		}
	}
	return &Error{Code: fallback}
}

func (engine *Engine) mark(ctx context.Context, id templatecatalog.ID, backupID string, state templatebackup.State) {
	_, _ = engine.backups.SetState(ctx, id, backupID, state)
}

type osReplacer struct{ catalog templatecatalog.Catalog }
type osReplacement struct {
	target              string
	prior, candidate    []byte
	candidateHash       string
	mode                os.FileMode
	uid, gid            int
	committed, restored bool
	applyMode           os.FileMode
	applyUID, applyGID  int
}

func (replacer osReplacer) Prepare(source, target string) (Replacement, error) {
	candidate, _, _, _, _, err := readRegular(source)
	if err != nil {
		return nil, err
	}
	prior, mode, uid, gid, _, err := readRegular(target)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(candidate)
	replacement := &osReplacement{target: target, prior: prior, candidate: candidate, candidateHash: hex.EncodeToString(sum[:]), mode: mode, uid: uid, gid: gid, applyMode: mode, applyUID: uid, applyGID: gid}
	for _, entry := range replacer.catalog.Entries() {
		if target == entry.SystemPath {
			m, err := filemetadata.Destination(entry.ID)
			if err != nil {
				return nil, err
			}
			replacement.applyMode, replacement.applyUID, replacement.applyGID = m.Mode, m.UID, m.GID
		}
	}
	return replacement, nil
}

func (replacement *osReplacement) Hash() string { return replacement.candidateHash }

func (replacement *osReplacement) Commit() error {
	if replacement.committed {
		return errors.New("replacement already committed")
	}
	if replacement.target == nativewrite.Target {
		if err := nativewrite.Replace(context.Background(), replacement.candidate, replacement.applyMode, replacement.applyUID, replacement.applyGID); err != nil {
			return err
		}
		replacement.committed = true
		return nil
	}
	path, err := stageFile(replacement.target, replacement.candidate, replacement.applyMode, replacement.applyUID, replacement.applyGID)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	if err = os.Rename(path, replacement.target); err != nil {
		return err
	}
	replacement.committed = true
	return syncDirectory(filepath.Dir(replacement.target))
}
func (replacement *osReplacement) Rollback() error {
	if !replacement.committed || replacement.restored {
		return nil
	}
	if err := atomicReplace(replacement.target, replacement.prior, replacement.mode, replacement.uid, replacement.gid); err != nil {
		return err
	}
	replacement.restored = true
	return nil
}
func (*osReplacement) Close() error { return nil }

func readRegular(path string) ([]byte, os.FileMode, int, int, string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, 0, 0, 0, "", errors.New("native file unavailable")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, 0, 0, "", err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, 0, 0, 0, "", errors.New("native file changed")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxApplyBytes+1))
	if err != nil || len(content) > maxApplyBytes {
		return nil, 0, 0, 0, "", errors.New("native file invalid")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || info.ModTime() != after.ModTime() {
		return nil, 0, 0, 0, "", errors.New("native file changed")
	}
	stat, ok := after.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, 0, 0, 0, "", errors.New("native metadata unavailable")
	}
	sum := sha256.Sum256(content)
	return content, after.Mode().Perm(), int(stat.Uid), int(stat.Gid), hex.EncodeToString(sum[:]), nil
}

func nativeHash(path string) (string, error) {
	_, _, _, _, hash, err := readRegular(path)
	return hash, err
}

func atomicReplace(target string, content []byte, mode os.FileMode, uid, gid int) error {
	if target == nativewrite.Target {
		return nativewrite.Replace(context.Background(), content, mode, uid, gid)
	}
	path, err := stageFile(target, content, mode, uid, gid)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	if err = os.Rename(path, target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func stageFile(target string, content []byte, mode os.FileMode, uid, gid int) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".apply-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	if err = file.Chmod(mode); err == nil {
		err = file.Chown(uid, gid)
	}
	if err == nil {
		_, err = file.Write(content)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
