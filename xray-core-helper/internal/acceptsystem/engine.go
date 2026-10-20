// Package acceptsystem implements System -> Template synchronization without touching runtime state.
package acceptsystem

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

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/nativeblock"
	"home-router/xray-core-helper/internal/templatebackup"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatecompare"
	"home-router/xray-core-helper/internal/templatevalidation"
)

type Code string

const (
	CodeUnknownTemplate    Code = "unknown_template"
	CodeStatusUnavailable  Code = "template_status_unavailable"
	CodeRevisionConflict   Code = "template_revision_conflict"
	CodeGenerationConflict Code = "template_generation_conflict"
	CodeSourceMissing      Code = "system_file_missing"
	CodeTargetMissing      Code = "template_file_missing"
	CodeInvalidSystem      Code = "invalid_system_content"
	CodeStateInvalid       Code = "template_state_invalid"
	CodeBackupFailed       Code = "template_backup_failed"
	CodeReplaceFailed      Code = "template_replace_failed"
	CodeCommitFailed       Code = "template_backup_commit_failed"
)

type Error struct{ Code Code }

func (failure *Error) Error() string { return string(failure.Code) }
func IsCode(err error, code Code) bool {
	var failure *Error
	return errors.As(err, &failure) && failure.Code == code
}

type Projection interface {
	Current() (api.TemplateStatusResponse, bool)
	Refresh(context.Context) api.TemplateStatusResponse
}
type Validator interface {
	Validate(context.Context, templatecatalog.ID, string) (templatevalidation.Result, error)
}
type BackupStore interface {
	Snapshot(context.Context, templatecatalog.ID, templatebackup.Side, templatebackup.State) (templatebackup.Record, error)
	SetState(context.Context, templatecatalog.ID, string, templatebackup.State) (templatebackup.Record, error)
	CompletedGeneration(context.Context, templatecatalog.ID) (uint64, error)
}
type Resolver interface {
	Lookup(templatecatalog.ID) (templatecatalog.Entry, error)
}
type Candidate interface {
	Path() string
	Commit(string) error
	Close() error
}
type Preparer interface {
	Prepare(templatecatalog.ID, string, string) (Candidate, error)
}

type Result struct {
	TemplateID string            `json:"templateId"`
	Accepted   bool              `json:"accepted"`
	BackupID   string            `json:"backupId,omitempty"`
	Generation uint64            `json:"generation"`
	Revision   string            `json:"revision"`
	State      api.TemplateState `json:"state"`
}
type Engine struct {
	mu         sync.Mutex
	projection Projection
	validator  Validator
	backups    BackupStore
	resolver   Resolver
	preparer   Preparer
	render     func(context.Context, templatecatalog.Entry, []byte) ([]byte, error)
}

func (engine *Engine) SetNativeRenderer(render func(context.Context, templatecatalog.Entry, []byte) ([]byte, error)) {
	engine.render = render
}

func New(projection Projection, validator Validator, backups BackupStore) (*Engine, error) {
	return NewWithCatalog(projection, validator, backups, templatecatalog.Default())
}

func NewWithCatalog(projection Projection, validator Validator, backups BackupStore, catalog templatecatalog.Catalog) (*Engine, error) {
	if projection == nil || validator == nil || backups == nil {
		return nil, &Error{Code: CodeStatusUnavailable}
	}
	return &Engine{projection: projection, validator: validator, backups: backups, resolver: catalogResolver{catalog}, preparer: osPreparer{}}, nil
}
func newEngine(projection Projection, validator Validator, backups BackupStore, resolver Resolver, preparer Preparer) (*Engine, error) {
	engine, err := New(projection, validator, backups)
	if err != nil {
		return nil, err
	}
	engine.resolver = resolver
	engine.preparer = preparer
	return engine, nil
}

func (engine *Engine) Accept(ctx context.Context, id templatecatalog.ID, expectedGeneration uint64, expectedRevision string) (Result, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	entry, err := engine.resolver.Lookup(id)
	if err != nil {
		return Result{}, &Error{Code: CodeUnknownTemplate}
	}
	status, err := engine.status(id, expectedRevision)
	if err != nil {
		return Result{}, err
	}
	generation, err := engine.backups.CompletedGeneration(ctx, id)
	if err != nil {
		return Result{}, &Error{Code: CodeBackupFailed}
	}
	if generation != expectedGeneration {
		return Result{}, &Error{Code: CodeGenerationConflict}
	}
	if status.State == api.TemplateSynchronized {
		return Result{TemplateID: string(id), Accepted: false, Generation: generation, Revision: status.Revision, State: status.State}, nil
	}
	switch status.State {
	case api.TemplateSystemMissing:
		return Result{}, &Error{Code: CodeSourceMissing}
	case api.TemplateMissing:
		return Result{}, &Error{Code: CodeTargetMissing}
	case api.TemplateInvalid:
		return Result{}, &Error{Code: CodeStateInvalid}
	case api.TemplateDifferent:
	default:
		return Result{}, &Error{Code: CodeStateInvalid}
	}
	if id == templatecatalog.Nftables {
		comparer, _ := templatecompare.New(nil)
		comparer.SetRenderer(engine.render)
		fresh := comparer.Compare(ctx, entry)
		if fresh.Revision != expectedRevision {
			return Result{}, &Error{Code: CodeRevisionConflict}
		}
		if fresh.Summary.BlockInitializationRequired {
			return Result{}, &Error{Code: CodeStateInvalid}
		}
	}
	candidate, err := engine.preparer.Prepare(id, entry.SystemPath, entry.WorkingPath)
	if err != nil {
		return Result{}, &Error{Code: CodeReplaceFailed}
	}
	defer candidate.Close()
	if _, err := engine.validator.Validate(ctx, id, candidate.Path()); err != nil {
		return Result{}, &Error{Code: CodeInvalidSystem}
	}
	backup, err := engine.backups.Snapshot(ctx, id, templatebackup.SideTemplate, templatebackup.StatePending)
	if err != nil {
		return Result{}, &Error{Code: CodeBackupFailed}
	}
	if err := candidate.Commit(backup.Hash); err != nil {
		return Result{}, &Error{Code: CodeReplaceFailed}
	}
	completed, err := engine.backups.SetState(ctx, id, backup.BackupID, templatebackup.StateCompleted)
	if err != nil {
		return Result{}, &Error{Code: CodeCommitFailed}
	}
	fresh := engine.projection.Refresh(ctx)
	next, ok := find(fresh, id)
	if !ok {
		return Result{}, &Error{Code: CodeStatusUnavailable}
	}
	return Result{TemplateID: string(id), Accepted: true, BackupID: backup.BackupID, Generation: completed.Generation, Revision: next.Revision, State: next.State}, nil
}
func (engine *Engine) status(id templatecatalog.ID, revision string) (api.TemplateStatus, error) {
	current, ok := engine.projection.Current()
	if !ok {
		return api.TemplateStatus{}, &Error{Code: CodeStatusUnavailable}
	}
	status, ok := find(current, id)
	if !ok {
		return api.TemplateStatus{}, &Error{Code: CodeStatusUnavailable}
	}
	if revision == "" || status.Revision != revision {
		return api.TemplateStatus{}, &Error{Code: CodeRevisionConflict}
	}
	return status, nil
}
func find(response api.TemplateStatusResponse, id templatecatalog.ID) (api.TemplateStatus, bool) {
	for _, status := range response.Templates {
		if status.TemplateID == string(id) {
			return status, true
		}
	}
	return api.TemplateStatus{}, false
}

type catalogResolver struct{ catalog templatecatalog.Catalog }

func (resolver catalogResolver) Lookup(id templatecatalog.ID) (templatecatalog.Entry, error) {
	return resolver.catalog.Lookup(id)
}

type osPreparer struct{}
type osCandidate struct {
	path, target string
	committed    bool
}

func (osPreparer) Prepare(id templatecatalog.ID, source, target string) (Candidate, error) {
	contents, _, err := read(source)
	if err != nil {
		return nil, err
	}
	if id == templatecatalog.Nftables {
		prior, _, err := read(target)
		if err != nil {
			return nil, err
		}
		merged, err := nativeblock.Build(prior, contents, false)
		if err != nil {
			return nil, err
		}
		contents = merged.Content
	}
	file, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".accept-*")
	if err != nil {
		return nil, err
	}
	candidate := &osCandidate{path: file.Name(), target: target}
	success := false
	defer func() {
		if !success {
			file.Close()
			os.Remove(file.Name())
		}
	}()
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(contents)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	success = true
	return candidate, nil
}
func (candidate *osCandidate) Path() string { return candidate.path }
func (candidate *osCandidate) Commit(expectedHash string) error {
	current, info, err := read(candidate.target)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(current)
	if hex.EncodeToString(sum[:]) != expectedHash {
		return errors.New("target changed")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("metadata unavailable")
	}
	if err := os.Chmod(candidate.path, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chown(candidate.path, int(stat.Uid), int(stat.Gid)); err != nil {
		return err
	}
	if candidate.committed {
		return errors.New("already committed")
	}
	file, err := os.Open(candidate.path)
	if err != nil {
		return err
	}
	err = file.Sync()
	_ = file.Close()
	if err != nil {
		return err
	}
	if err = os.Rename(candidate.path, candidate.target); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(candidate.target))
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		candidate.committed = true
	}
	return err
}
func sameSnapshot(before, after os.FileInfo) bool {
	left, leftOK := before.Sys().(*syscall.Stat_t)
	right, rightOK := after.Sys().(*syscall.Stat_t)
	return leftOK && rightOK && left.Dev == right.Dev && left.Ino == right.Ino && left.Size == right.Size && left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func (candidate *osCandidate) Close() error {
	if candidate.committed {
		return nil
	}
	return os.Remove(candidate.path)
}
func read(path string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, errors.New("source unavailable")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 16<<20+1))
	if err != nil || len(contents) > 16<<20 {
		return nil, nil, errors.New("source invalid")
	}
	after, err := file.Stat()
	if err != nil || !sameSnapshot(info, after) {
		return nil, nil, errors.New("source changed")
	}
	return contents, after, nil
}
