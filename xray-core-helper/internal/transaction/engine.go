package transaction

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/nativebinding"
)

const maxTransactionConfigBytes = 16 << 20

type Validator interface {
	Validate(context.Context, string) error
}

type Restarter interface {
	Restart(context.Context) error
}

type MutationPreparation interface {
	Commit() error
	Rollback() error
}

type PrepareMutation func(context.Context) (MutationPreparation, error)

type Engine struct {
	configPath string
	store      *Store
	validator  Validator
	restarter  Restarter
}

type fileSnapshot struct {
	content []byte
	hash    string
	mode    os.FileMode
	uid     int
	group   int
}

func NewEngine(configPath string, store *Store, validator Validator, restarter Restarter) (*Engine, error) {
	if !filepath.IsAbs(configPath) {
		return nil, errors.New("Xray configuration path must be absolute")
	}
	if store == nil || validator == nil {
		return nil, errors.New("transaction store and validator are required")
	}
	return &Engine{configPath: configPath, store: store, validator: validator, restarter: restarter}, nil
}

func (engine *Engine) Initialize(ctx context.Context) error {
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	snapshot, err := readSnapshot(engine.configPath)
	if err != nil {
		return wrapOperation("read initial Xray configuration", err)
	}
	metadata, err := engine.store.Initialize(ctx, snapshot.hash)
	if err != nil {
		return wrapOperation("initialize configuration metadata", err)
	}
	if metadata.ContentHash == snapshot.hash {
		return engine.saveAccepted(snapshot)
	}
	return nil
}

// Planner computes edits from the bytes read under the mutation lock.
type Planner func(source []byte) ([]ByteEdit, error)

func (engine *Engine) Apply(ctx context.Context, expectedGeneration uint64, edits []ByteEdit, restart bool) (Metadata, error) {
	return engine.Mutate(ctx, expectedGeneration, func([]byte) ([]ByteEdit, error) { return edits, nil }, restart)
}

func (engine *Engine) Mutate(ctx context.Context, expectedGeneration uint64, plan Planner, restart bool) (Metadata, error) {
	return engine.mutate(ctx, expectedGeneration, plan, restart, nil)
}

// MutateWithPreparation holds the process-wide mutation gate while preparing
// external resources, applying the common config transaction, and finalizing
// or rolling back the preparation snapshot.
func (engine *Engine) MutateWithPreparation(
	ctx context.Context,
	expectedGeneration uint64,
	plan Planner,
	restart bool,
	prepare PrepareMutation,
) (result Metadata, resultErr error) {
	if prepare == nil {
		return Metadata{}, errors.New("mutation preparation is required")
	}
	return engine.mutate(ctx, expectedGeneration, plan, restart, prepare)
}

// ConfirmedLoggingOptions is used only for an explicitly acknowledged logging
// path restriction. It does not change normal config transaction semantics.
type ConfirmedLoggingOptions struct {
	Enabled func() bool
}

func (engine *Engine) MutateConfirmedLogging(ctx context.Context, generation uint64, plan Planner, restart bool, prepare PrepareMutation, options ConfirmedLoggingOptions) (Metadata, error) {
	if prepare == nil || options.Enabled == nil {
		return Metadata{}, errors.New("confirmed logging dependencies are required")
	}
	return engine.mutateOptions(ctx, generation, plan, restart, prepare, &options, false)
}

func (engine *Engine) mutate(
	ctx context.Context,
	expectedGeneration uint64,
	plan Planner,
	restart bool,
	prepare PrepareMutation,
) (result Metadata, resultErr error) {
	return engine.mutateOptions(ctx, expectedGeneration, plan, restart, prepare, nil, false)
}

func (engine *Engine) mutateOptions(ctx context.Context, expectedGeneration uint64, plan Planner, restart bool, prepare PrepareMutation, logging *ConfirmedLoggingOptions, restoreExternal bool) (result Metadata, resultErr error) {
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return Metadata{}, err
	}
	defer lease.Release()
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		engine.pruneAccepted(cleanupCtx)
	}()
	var preparation MutationPreparation
	committedConfig := false
	defer func() {
		if preparation == nil {
			return
		}
		if resultErr != nil && !committedConfig {
			if rollbackErr := preparation.Rollback(); rollbackErr != nil {
				coordinator.Block()
				result = Metadata{}
				resultErr = publicError("recovery_required", "mutation recovery is required", 503,
					fmt.Errorf("%w: %v", ErrRecoveryRequired, rollbackErr))
			}
			return
		}
		if commitErr := preparation.Commit(); commitErr != nil {
			rollbackErr := preparation.Rollback()
			coordinator.Block()
			result = Metadata{}
			resultErr = publicError("recovery_required", "mutation recovery is required", 503,
				fmt.Errorf("%w: %v", ErrRecoveryRequired, errors.Join(commitErr, rollbackErr)))
		}
	}()
	metadata, err := engine.store.Metadata(ctx)
	if err != nil {
		return Metadata{}, wrapOperation("read committed configuration metadata", err)
	}
	if metadata.Generation != expectedGeneration {
		return Metadata{}, ErrStaleGeneration
	}
	if restart && engine.restarter == nil {
		return Metadata{}, ErrRestartUnavailable
	}
	if err := engine.ensureNoPending(ctx); err != nil {
		return Metadata{}, err
	}
	previous, err := readSnapshot(engine.configPath)
	if err != nil {
		return Metadata{}, wrapOperation("read current Xray configuration", err)
	}
	if previous.hash != metadata.ContentHash && !restoreExternal {
		return Metadata{}, ErrStateDrift
	}
	edits, err := plan(previous.content)
	if err != nil {
		return Metadata{}, err
	}
	if logging != nil && !logging.Enabled() {
		logging = nil
	}
	candidate, err := ApplyEdits(previous.content, edits)
	if err != nil {
		return Metadata{}, publicError("invalid_edit", "configuration edit is invalid", 400, err)
	}
	if len(candidate) > maxTransactionConfigBytes {
		return Metadata{}, publicError("invalid_edit", "candidate configuration exceeds the size limit", 413, nil)
	}
	nextHash := hashBytes(candidate)
	if prepare != nil {
		preparation, err = prepare(ctx)
		if err != nil {
			return Metadata{}, err
		}
		if preparation == nil {
			return Metadata{}, errors.New("mutation preparation returned no snapshot")
		}
	}
	if nextHash == previous.hash {
		if logging != nil && restart {
			committedConfig = true
			if engine.restarter.Restart(ctx) != nil {
				return metadata, savedRestartError()
			}
		}
		return metadata, nil
	}
	transactionID, err := randomID()
	if err != nil {
		return Metadata{}, wrapOperation("create transaction identifier", err)
	}
	configDir := filepath.Dir(engine.configPath)
	baseName := filepath.Base(engine.configPath)
	candidatePath := filepath.Join(configDir, "."+baseName+".xch-candidate-"+transactionID+filepath.Ext(baseName))
	backupPath := filepath.Join(configDir, "."+baseName+".xch-backup-"+transactionID)
	if err := writeArtifact(candidatePath, candidate, previous.mode, previous.uid, previous.group); err != nil {
		return Metadata{}, wrapOperation("write candidate configuration", err)
	}
	defer removeArtifact(candidatePath)
	if err := engine.validator.Validate(ctx, candidatePath); err != nil {
		if errors.Is(err, ErrMarkupInvalid) {
			return Metadata{}, ErrMarkupInvalid
		}
		return Metadata{}, publicError("validation_failed", "Xray rejected the candidate configuration", 422, ErrValidationFailed)
	}
	current, err := readSnapshot(engine.configPath)
	if err != nil {
		return Metadata{}, wrapOperation("recheck active configuration", err)
	}
	if current.hash != previous.hash {
		return Metadata{}, ErrStateDrift
	}
	if previous.hash == metadata.ContentHash {
		if err := engine.saveAccepted(previous); err != nil {
			return Metadata{}, err
		}
	}
	acceptedCandidate := previous
	acceptedCandidate.content = candidate
	acceptedCandidate.hash = nextHash
	if err := engine.saveAccepted(acceptedCandidate); err != nil {
		return Metadata{}, err
	}
	if err := writeArtifact(backupPath, previous.content, previous.mode, previous.uid, previous.group); err != nil {
		return Metadata{}, wrapOperation("write configuration backup", err)
	}
	journalSaved := false
	defer func() {
		if !journalSaved {
			removeArtifact(backupPath)
		}
	}()
	if err := syncDirectory(configDir); err != nil {
		return Metadata{}, wrapOperation("sync configuration artifacts", err)
	}
	pending := Pending{
		ID:                 transactionID,
		Phase:              "staged",
		PreviousGeneration: metadata.Generation,
		PreviousHash:       previous.hash,
		PreviousStateHash:  metadata.ContentHash,
		NextHash:           nextHash,
		CandidatePath:      candidatePath,
		BackupPath:         backupPath,
		RestartRequired:    restart,
	}
	if err := engine.store.SavePending(ctx, pending); err != nil {
		if stored, pendingErr := engine.store.Pending(ctx); pendingErr == nil && stored != nil && stored.ID == transactionID {
			journalSaved = true
		}
		return Metadata{}, wrapOperation("record configuration transaction", err)
	}
	journalSaved = true
	if err := ctx.Err(); err != nil {
		return Metadata{}, engine.failAndRollback(ctx, pending, false, err)
	}
	if err := os.Rename(candidatePath, engine.configPath); err != nil {
		return Metadata{}, engine.failAndRollback(ctx, pending, false, err)
	}
	if err := syncDirectory(configDir); err != nil {
		return Metadata{}, engine.failAndRollback(ctx, pending, false, err)
	}
	if err := engine.store.Advance(ctx, transactionID, "staged", "replaced"); err != nil {
		return Metadata{}, engine.failAndRollback(ctx, pending, false, err)
	}
	restartFailed := false
	if restart {
		if err := engine.store.Advance(ctx, transactionID, "replaced", "restart_started"); err != nil {
			return Metadata{}, engine.failAndRollback(ctx, pending, false, err)
		}
		if err := engine.restarter.Restart(ctx); err != nil {
			if logging == nil {
				return Metadata{}, engine.failAndRollback(ctx, pending, true, ErrRestartFailed)
			}
			restartFailed = true
		}
	}
	commitCtx := ctx
	if restartFailed {
		// The acknowledged restart failure must not turn into an implicit
		// rollback merely because the request deadline expired during restart.
		var cancel context.CancelFunc
		commitCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
	}
	committed, err := engine.store.CommitPending(commitCtx, transactionID)
	if err != nil {
		currentState, stateErr := engine.store.Metadata(commitCtx)
		if stateErr == nil && currentState.Generation == metadata.Generation+1 && currentState.ContentHash == nextHash {
			committedConfig = true
			if restartFailed {
				return currentState, savedRestartError()
			}
			return currentState, nil
		}
		return Metadata{}, engine.failAndRollback(ctx, pending, restart, err)
	}
	_ = syncDirectory(configDir)
	committedConfig = true
	if restartFailed {
		return committed, savedRestartError()
	}
	return committed, nil
}

func savedRestartError() error {
	return publicError("logging_saved_restart_failed", "Logging settings were saved; Xray could not restart. Correct log access or service restrictions manually.", 503, nil)
}

func (engine *Engine) Adopt(ctx context.Context, expectedGeneration uint64) (Metadata, bool, error) {
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return Metadata{}, false, err
	}
	defer lease.Release()
	metadata, err := engine.store.Metadata(ctx)
	if err != nil {
		return Metadata{}, false, wrapOperation("read committed configuration metadata", err)
	}
	if metadata.Generation != expectedGeneration {
		return Metadata{}, false, ErrStaleGeneration
	}
	if err := engine.ensureNoPending(ctx); err != nil {
		return Metadata{}, false, err
	}
	snapshot, err := readSnapshot(engine.configPath)
	if err != nil {
		return Metadata{}, false, wrapOperation("read external Xray configuration", err)
	}
	if snapshot.hash == metadata.ContentHash {
		return metadata, false, nil
	}
	log.Print("xray config drift detected; validating current configuration and helper annotations")
	if err := engine.validator.Validate(ctx, engine.configPath); err != nil {
		if errors.Is(err, ErrMarkupInvalid) {
			log.Print("xray config adoption rejected: helper annotations are invalid")
			return Metadata{}, false, publicError("config_markup_invalid", "Xray configuration helper annotations are invalid; configuration was not adopted", 422, ErrMarkupInvalid)
		}
		log.Print("xray config adoption rejected: Xray validation failed")
		return Metadata{}, false, publicError("validation_failed", "Xray rejected the external configuration; configuration was not adopted", 422, ErrValidationFailed)
	}
	log.Print("xray config validation passed")

	confirmed, err := readSnapshot(engine.configPath)
	if err != nil {
		return Metadata{}, false, wrapOperation("recheck external Xray configuration", err)
	}
	if confirmed.hash != snapshot.hash {
		log.Print("xray config adoption cancelled: configuration changed during validation")
		return Metadata{}, false, ErrStateDrift
	}
	previousHash := ""
	if prior, err := readSnapshot(engine.acceptedPath(metadata.ContentHash)); err == nil && prior.hash == metadata.ContentHash {
		previousHash = metadata.ContentHash
	}
	if err := engine.saveAccepted(confirmed); err != nil {
		return Metadata{}, false, err
	}
	adopted, err := engine.store.adopt(ctx, expectedGeneration, confirmed.hash, previousHash)
	if err != nil {
		return Metadata{}, false, err
	}
	engine.pruneAccepted(ctx)
	log.Printf("xray config adopted; generation=%d", adopted.Generation)
	return adopted, true, nil
}

// ReconcileDrift validates and adopts external edits when state is requested.
// It never rewrites the config or restarts Xray. Pending recovery is left alone.
func (engine *Engine) ReconcileDrift(ctx context.Context) error {
	metadata, err := engine.store.Metadata(ctx)
	if err != nil {
		return err
	}
	snapshot, err := readSnapshot(engine.configPath)
	if err != nil {
		return err
	}
	if snapshot.hash == metadata.ContentHash {
		return nil
	}
	_, _, err = engine.Adopt(ctx, metadata.Generation)
	return err
}

func (engine *Engine) Recover(ctx context.Context) error {
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	metadata, err := engine.store.Metadata(ctx)
	if err != nil {
		return wrapOperation("read recovery metadata", err)
	}
	if metadata.ContentHash == "" {
		return publicError("recovery_required", "configuration recovery state is incomplete", 503, ErrRecoveryRequired)
	}
	pending, err := engine.store.Pending(ctx)
	if err != nil {
		return wrapOperation("read transaction journal", err)
	}
	if pending == nil {
		return nil
	}
	if metadata.Generation != pending.PreviousGeneration || metadata.ContentHash != pending.stateHash() {
		return engine.recoveryError(ErrStateDrift)
	}
	if !engine.validArtifactPath(pending.CandidatePath, "candidate") || !engine.validArtifactPath(pending.BackupPath, "backup") {
		return publicError("recovery_required", "transaction journal contains unsafe artifact paths", 503, ErrRecoveryRequired)
	}
	current, err := readSnapshot(engine.configPath)
	if err != nil {
		return engine.recoveryError(err)
	}
	switch current.hash {
	case pending.PreviousHash:
		if pending.Phase == "restart_started" && pending.RestartRequired {
			if err := engine.restartForRecovery(ctx); err != nil {
				return engine.recoveryError(err)
			}
		}
	case pending.NextHash:
		backup, err := readSnapshot(pending.BackupPath)
		if err != nil || backup.hash != pending.PreviousHash {
			return engine.recoveryError(errors.New("transaction backup is missing or invalid"))
		}
		if err := restoreSnapshot(engine.configPath, backup); err != nil {
			return engine.recoveryError(err)
		}
		if pending.Phase == "restart_started" && pending.RestartRequired {
			if err := engine.restartForRecovery(ctx); err != nil {
				return engine.recoveryError(err)
			}
		}
	default:
		return ErrStateDrift
	}
	if err := engine.store.ClearPending(ctx, pending.ID); err != nil {
		return engine.recoveryError(err)
	}
	engine.cleanupPending(*pending)
	return nil
}

func (engine *Engine) failAndRollback(ctx context.Context, pending Pending, restartMayHaveRun bool, cause error) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := engine.rollback(rollbackCtx, pending, restartMayHaveRun); err != nil {
		return publicError("rollback_failed", "configuration rollback failed; recovery is required", 500, fmt.Errorf("%v: %w", cause, ErrRollbackFailed))
	}
	if errors.Is(cause, ErrRestartFailed) {
		return ErrRestartFailed
	}
	return wrapOperation("configuration transaction", cause)
}

func (engine *Engine) rollback(ctx context.Context, pending Pending, restart bool) error {
	backup, err := readSnapshot(pending.BackupPath)
	if err != nil || backup.hash != pending.PreviousHash {
		return ErrRollbackFailed
	}
	current, err := readSnapshot(engine.configPath)
	if err != nil {
		return err
	}
	if current.hash == pending.NextHash {
		if err := restoreSnapshot(engine.configPath, backup); err != nil {
			return err
		}
	} else if current.hash != pending.PreviousHash {
		return ErrStateDrift
	}
	if restart {
		if err := engine.restartForRecovery(ctx); err != nil {
			return err
		}
	}
	if err := engine.store.ClearPending(ctx, pending.ID); err != nil {
		return err
	}
	engine.cleanupPending(pending)
	return nil
}

func (engine *Engine) restartForRecovery(ctx context.Context) error {
	if engine.restarter == nil {
		return ErrRestartUnavailable
	}
	if err := engine.restarter.Restart(ctx); err != nil {
		return ErrRestartFailed
	}
	return nil
}

func (engine *Engine) recoveryError(cause error) error {
	return publicError("recovery_required", "configuration transaction recovery is required", 503, fmt.Errorf("%v: %w", cause, ErrRecoveryRequired))
}

func (engine *Engine) ensureNoPending(ctx context.Context) error {
	pending, err := engine.store.Pending(ctx)
	if err != nil {
		return wrapOperation("check pending configuration transaction", err)
	}
	if pending != nil {
		return ErrTransactionBusy
	}
	return nil
}

func (engine *Engine) validArtifactPath(path, kind string) bool {
	if filepath.Dir(path) != filepath.Dir(engine.configPath) {
		return false
	}
	prefix := "." + filepath.Base(engine.configPath) + ".xch-" + kind + "-"
	if !strings.HasPrefix(filepath.Base(path), prefix) {
		return false
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && kind == "candidate" {
		return true
	}
	return err == nil && info.Mode().IsRegular()
}

func (engine *Engine) cleanupPending(pending Pending) {
	removeArtifact(pending.CandidatePath)
	removeArtifact(pending.BackupPath)
	_ = syncDirectory(filepath.Dir(engine.configPath))
}

func readSnapshot(path string) (fileSnapshot, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fileSnapshot{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fileSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return fileSnapshot{}, errors.New("configuration file is not regular")
	}
	if info.Size() > maxTransactionConfigBytes {
		return fileSnapshot{}, errors.New("configuration file exceeds the size limit")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileSnapshot{}, errors.New("configuration ownership metadata is unavailable")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxTransactionConfigBytes+1))
	if err != nil {
		return fileSnapshot{}, err
	}
	if len(content) > maxTransactionConfigBytes {
		return fileSnapshot{}, errors.New("configuration file exceeds the size limit")
	}
	return fileSnapshot{
		content: content,
		hash:    hashBytes(content),
		mode:    info.Mode().Perm() | info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky),
		uid:     int(stat.Uid),
		group:   int(stat.Gid),
	}, nil
}

func writeArtifact(path string, content []byte, mode os.FileMode, uid, group int) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if uid != os.Geteuid() || group != os.Getegid() {
		if err := file.Chown(uid, group); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return err
		}
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func restoreSnapshot(target string, backup fileSnapshot) error {
	id, err := randomID()
	if err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".xch-rollback-"+id)
	if err := writeArtifact(temporary, backup.content, backup.mode, backup.uid, backup.group); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func randomID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}

func hashBytes(content []byte) string {
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:])
}

func removeArtifact(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type XrayValidator struct {
	binary   string
	assetDir string
}

func NewXrayValidator(binary string, assetDir string) (*XrayValidator, error) {
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary {
		return nil, errors.New("Xray binary path must be absolute and clean")
	}
	if !filepath.IsAbs(assetDir) || filepath.Clean(assetDir) != assetDir {
		return nil, errors.New("Xray asset directory must be absolute and clean")
	}
	return &XrayValidator{binary: binary, assetDir: assetDir}, nil
}

func (validator *XrayValidator) Validate(ctx context.Context, configPath string) error {
	if _, err := nativebinding.ParseFile(configPath); err != nil {
		return ErrMarkupInvalid
	}

	command := exec.CommandContext(ctx, validator.binary, "run", "-test", "-config", configPath)
	command.Env = []string{"XRAY_LOCATION_ASSET=" + validator.assetDir}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return ErrValidationFailed
	}
	return nil
}
