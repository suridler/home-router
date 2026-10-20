package templateapply

import (
	"context"

	"home-router/xray-core-helper/internal/templatebackup"
	"home-router/xray-core-helper/internal/templatecatalog"
)

type ConfirmedDescriptor struct {
	TemplateID    templatecatalog.ID
	BackupID      string
	CandidateHash string
	Generation    uint64
}

// RollbackNeedsReboot is a trusted lifecycle policy, not inferred nft semantics.
// A file restore cannot undo arbitrary opaque native commands in the kernel.
func (engine *Engine) RollbackNeedsReboot(id templatecatalog.ID) bool {
	return systemMember(id)
}

func (engine *Engine) CompleteRebootRollback(ctx context.Context, descriptor ConfirmedDescriptor) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if !systemMember(descriptor.TemplateID) {
		return &Error{Code: CodeUnknownTemplate}
	}
	if _, err := engine.backups.SetState(ctx, descriptor.TemplateID, descriptor.BackupID, templatebackup.StateRollback); err != nil {
		return &Error{Code: CodeBackup}
	}
	return nil
}

func (engine *Engine) PrepareConfirmed(ctx context.Context, id templatecatalog.ID, expectedGeneration uint64) (ConfirmedDescriptor, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	entry, err := engine.catalog.Lookup(id)
	if err != nil {
		return ConfirmedDescriptor{}, &Error{Code: CodeUnknownTemplate}
	}
	generation, err := engine.backups.CompletedGeneration(ctx, id)
	if err != nil {
		return ConfirmedDescriptor{}, &Error{Code: CodeBackup}
	}
	if generation != expectedGeneration {
		return ConfirmedDescriptor{}, &Error{Code: CodeGeneration}
	}
	candidate, err := engine.candidate(ctx, entry, true)
	if err != nil {
		return ConfirmedDescriptor{}, err
	}
	defer candidate.close()
	if err = engine.check.Check(ctx, id, candidate.path); err != nil {
		return ConfirmedDescriptor{}, err
	}
	current, err := engine.hash(candidate.path)
	if err != nil || current != candidate.hash {
		return ConfirmedDescriptor{}, &Error{Code: CodeValidation}
	}
	var lifecycle lifecyclePrepared
	if id == templatecatalog.TProxyUnit {
		lifecycle, err = engine.prepareLifecycle()
		if err != nil {
			return ConfirmedDescriptor{}, err
		}
		if len(engine.lifecycleBaseline) > 0 {
			if len(engine.lifecycleBaseline) != len(lifecycle.prior) {
				return ConfirmedDescriptor{}, &Error{Code: CodeValidation}
			}
			for i, prior := range lifecycle.prior {
				if !baselineEqual(prior, engine.lifecycleBaseline[i]) {
					return ConfirmedDescriptor{}, &Error{Code: CodeValidation}
				}
			}
		}
	}
	backup, err := engine.backups.Snapshot(ctx, id, templatebackup.SideSystem, templatebackup.StatePending)
	if err != nil {
		return ConfirmedDescriptor{}, &Error{Code: CodeBackup}
	}
	if len(lifecycle.assets) > 0 {
		engine.lifecyclePlans = map[string]lifecyclePrepared{backup.BackupID: lifecycle}
	}
	return ConfirmedDescriptor{TemplateID: id, BackupID: backup.BackupID, CandidateHash: lifecycleHash(candidate.hash, lifecycle), Generation: backup.Generation}, nil
}

func (engine *Engine) ActivateConfirmed(ctx context.Context, descriptor ConfirmedDescriptor) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	entry, err := engine.catalog.Lookup(descriptor.TemplateID)
	if err != nil {
		return &Error{Code: CodeUnknownTemplate}
	}
	candidate, err := engine.candidate(ctx, entry, false)
	if err != nil {
		return err
	}
	defer candidate.close()
	var lifecycle lifecyclePrepared
	if descriptor.TemplateID == templatecatalog.TProxyUnit {
		lifecycle, err = engine.prepareLifecycle()
		if err != nil {
			return err
		}
	}
	if lifecycleHash(candidate.hash, lifecycle) != descriptor.CandidateHash {
		return &Error{Code: CodeValidation}
	}
	runtime, err := engine.runtime.Prepare(ctx, descriptor.TemplateID)
	if err != nil {
		return &Error{Code: CodeRuntime}
	}
	if descriptor.TemplateID == templatecatalog.Nftables {
		if err = runtime.Apply(ctx, candidate.path); err == nil {
			err = runtime.Healthy(ctx)
		}
		if err != nil {
			// Service owns durable file-only rollback and the reboot-required
			// state. Never pretend reloading the old file restores live rules.
			return effectError(err, CodeRuntime)
		}
		return nil
	}
	replacement, err := engine.replacer.Prepare(candidate.path, entry.SystemPath)
	if err != nil {
		return &Error{Code: CodeReplace}
	}
	defer replacement.Close()
	if replacement.Hash() != candidate.hash {
		return &Error{Code: CodeValidation}
	}
	if quiescer, ok := runtime.(interface{ BeforeReplace(context.Context) error }); ok {
		if err = quiescer.BeforeReplace(ctx); err != nil {
			return effectError(err, CodeRuntime)
		}
	}
	if descriptor.TemplateID == templatecatalog.TProxyUnit && len(lifecycle.assets) > 0 {
		prior, ok := engine.lifecyclePlans[descriptor.BackupID]
		if !ok {
			return &Error{Code: CodeValidation}
		}
		// The source digest above is durable; the original target snapshots are
		// used only to reject drift before this synchronous activation.
		lifecycle.prior = prior.prior
		if err = engine.installLifecycle(ctx, lifecycle); err != nil {
			return err
		}
		delete(engine.lifecyclePlans, descriptor.BackupID)
	}
	if err = replacement.Commit(); err == nil {
		err = runtime.Apply(ctx, entry.SystemPath)
	}
	if err == nil {
		err = runtime.Healthy(ctx)
	}
	if err != nil {
		if systemMember(descriptor.TemplateID) {
			// The safe-apply service restores the complete durable group. Never
			// attempt component-specific live rollback or release its evidence.
			return effectError(err, CodeRuntime)
		}
		fileErr := replacement.Rollback()
		runtimeErr := runtime.Rollback(context.WithoutCancel(ctx), entry.SystemPath)
		state := templatebackup.StateRollback
		if fileErr != nil || runtimeErr != nil {
			state = templatebackup.StateRecoveryRequired
		}
		engine.mark(context.WithoutCancel(ctx), descriptor.TemplateID, descriptor.BackupID, state)
		if state == templatebackup.StateRecoveryRequired {
			return &Error{Code: CodeRollback}
		}
		return effectError(err, CodeRuntime)
	}
	return nil
}

func (engine *Engine) Confirm(ctx context.Context, descriptor ConfirmedDescriptor) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	entry, err := engine.catalog.Lookup(descriptor.TemplateID)
	if err != nil {
		return &Error{Code: CodeUnknownTemplate}
	}
	if descriptor.TemplateID == templatecatalog.Nftables {
		candidate, err := engine.candidate(ctx, entry, false)
		if err != nil {
			return err
		}
		defer candidate.close()
		if candidate.hash != descriptor.CandidateHash {
			return &Error{Code: CodeValidation}
		}
		replacement, err := engine.replacer.Prepare(candidate.path, entry.SystemPath)
		if err != nil {
			return &Error{Code: CodeReplace}
		}
		defer replacement.Close()
		if replacement.Hash() != descriptor.CandidateHash {
			return &Error{Code: CodeValidation}
		}
		if err = replacement.Commit(); err != nil {
			return &Error{Code: CodeReplace}
		}
	} else {
		hash, err := engine.hash(entry.SystemPath)
		var lifecycle lifecyclePrepared
		if descriptor.TemplateID == templatecatalog.TProxyUnit {
			lifecycle, err = engine.prepareLifecycle()
			if err != nil {
				return err
			}
			if err = engine.verifyLifecycle(lifecycle); err != nil {
				return err
			}
		}
		if err != nil || lifecycleHash(hash, lifecycle) != descriptor.CandidateHash {
			return &Error{Code: CodeValidation}
		}
	}
	if _, err = engine.backups.SetState(ctx, descriptor.TemplateID, descriptor.BackupID, templatebackup.StateCompleted); err != nil {
		return &Error{Code: CodeBackup}
	}
	return nil
}

func (engine *Engine) RollbackConfirmed(ctx context.Context, descriptor ConfirmedDescriptor) error {
	if systemMember(descriptor.TemplateID) {
		return &Error{Code: CodeSafeApply} // service requires complete group evidence
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	entry, err := engine.catalog.Lookup(descriptor.TemplateID)
	if err != nil {
		return &Error{Code: CodeUnknownTemplate}
	}
	if _, err = engine.backups.RestorePending(ctx, descriptor.TemplateID, descriptor.BackupID); err != nil {
		return &Error{Code: CodeRollback}
	}
	runtime, err := engine.runtime.Prepare(ctx, descriptor.TemplateID)
	if err != nil {
		return &Error{Code: CodeRollback}
	}
	if err = runtime.Rollback(ctx, entry.SystemPath); err != nil {
		return &Error{Code: CodeRollback}
	}
	if _, err = engine.backups.SetState(ctx, descriptor.TemplateID, descriptor.BackupID, templatebackup.StateRollback); err != nil {
		return &Error{Code: CodeRollback}
	}
	return nil
}
