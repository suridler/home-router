package templateapply

import (
	"context"

	"home-router/xray-core-helper/internal/templatebackup"
)

// RecoverConfirmed restores the durable files and the prior runtime state.
// Inactive systemd services stay stopped, which avoids early-boot ordering cycles.
func (engine *Engine) RecoverConfirmed(ctx context.Context, descriptor ConfirmedDescriptor) error {
	if systemMember(descriptor.TemplateID) {
		return &Error{Code: CodeSafeApply} // never invent a missing group baseline
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
	if _, err := engine.backups.SetState(ctx, descriptor.TemplateID, descriptor.BackupID, templatebackup.StateRollback); err != nil {
		return &Error{Code: CodeRollback}
	}
	return nil
}
