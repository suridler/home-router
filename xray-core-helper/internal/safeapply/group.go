package safeapply

import (
	"context"

	"home-router/xray-core-helper/internal/templateapply"
)

// The production engine implements this private file-only capability. Test
// engines without it exercise the independent legacy/Xray state machine.
type groupEngine interface {
	CaptureSystemBaseline(context.Context) ([]templateapply.BaselineFile, error)
	RestoreSystemFile(context.Context, templateapply.BaselineFile) error
	VerifySystemBaseline(context.Context, []templateapply.BaselineFile) error
}

func (service *Service) restoreGroup(ctx context.Context, transaction Transaction) (bool, error) {
	engine, ok := service.engine.(groupEngine)
	if !ok || !systemMember(transaction.TemplateID) {
		return false, nil
	}
	files, err := service.store.Baseline(ctx, transaction.ID)
	if err != nil {
		return true, ErrBaseline
	}
	for _, file := range files {
		if err := engine.RestoreSystemFile(ctx, file); err != nil {
			return true, err
		}
		if _, err := service.store.db.ExecContext(ctx, `INSERT OR IGNORE INTO safe_apply_restore(transaction_id,template_id) VALUES(?,?)`, transaction.ID, file.TemplateID); err != nil {
			return true, err
		}
	}
	// Always re-restore/recheck on retry rather than trust progress from a prior
	// boot or ignore external edits made after an earlier member was restored.
	return true, engine.VerifySystemBaseline(ctx, files)
}

func (service *Service) verifyGroup(ctx context.Context, transaction Transaction) error {
	engine, ok := service.engine.(groupEngine)
	if !ok || !systemMember(transaction.TemplateID) {
		return nil
	}
	files, err := service.store.Baseline(ctx, transaction.ID)
	if err != nil {
		return ErrBaseline
	}
	return engine.VerifySystemBaseline(ctx, files)
}
