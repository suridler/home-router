package safeapply

import (
	"context"
	"errors"

	"home-router/xray-core-helper/internal/templateapply"
	"home-router/xray-core-helper/internal/templatecatalog"
)

const maxBaselineBytes = 1 << 20

// BaselineFile is private recovery evidence, never an API projection. An absent
// file has no content or metadata. Contents live in the existing private DB and
// cannot be pruned independently by per-template backup retention.
type BaselineFile = templateapply.BaselineFile

var ErrBaseline = errors.New("invalid or unavailable system recovery baseline")

// Keep the latest configured number of terminal operation baselines, plus all
// pending evidence. Progress rows disappear in the same SQLite transaction.
func (store *Store) pruneBaselines(ctx context.Context, retain int) error {
	if retain < 1 || retain > 1000 {
		return ErrBaseline
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	const old = `SELECT transaction_id FROM safe_apply_transaction WHERE state!='pending' AND transaction_id IN (SELECT transaction_id FROM safe_apply_baseline) ORDER BY deadline_ns DESC,transaction_id DESC LIMIT -1 OFFSET ?`
	if _, err = tx.ExecContext(ctx, `DELETE FROM safe_apply_restore WHERE transaction_id IN (`+old+`)`, retain); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM safe_apply_baseline WHERE transaction_id IN (`+old+`)`, retain); err != nil {
		return err
	}
	return tx.Commit()
}

func systemMember(id templatecatalog.ID) bool {
	return id == templatecatalog.Nftables || id == templatecatalog.Sysctl || id == templatecatalog.TProxyUnit
}

func validateBaseline(files []BaselineFile) error {
	return validateBaselineGroup(files, false)
}

func validateBaselineGroup(files []BaselineFile, legacy bool) error {
	if len(files) != 4 && !(legacy && len(files) == 3) {
		return ErrBaseline
	}
	seen := make(map[templatecatalog.ID]bool, len(files))
	for _, file := range files {
		if (!systemMember(file.TemplateID) && (len(files) != 4 || !templateapply.LifecycleMember(file.TemplateID))) || seen[file.TemplateID] || len(file.Content) > maxBaselineBytes || file.Mode & ^uint32(0777) != 0 {
			return ErrBaseline
		}
		if !file.Present && (len(file.Content) != 0 || file.Mode != 0 || file.UID != 0 || file.GID != 0) {
			return ErrBaseline
		}
		seen[file.TemplateID] = true
	}
	return nil
}

// RecordBaseline atomically attaches a complete immutable group to a prepared
// operation. Callers must capture every file before this call and must not
// mutate the system until it succeeds. This storage foundation is not yet an
// activation API: legacy operations without a group remain distinguishable.
func (store *Store) RecordBaseline(ctx context.Context, transactionID string, files []BaselineFile) error {
	if err := validateBaseline(files); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id templatecatalog.ID
	var phase Phase
	var state State
	if err := tx.QueryRowContext(ctx, `SELECT template_id,phase,state FROM safe_apply_transaction WHERE transaction_id=?`, transactionID).Scan(&id, &phase, &state); err != nil {
		return ErrBaseline
	}
	if !systemMember(id) || phase != PhasePrepared || state != StatePending {
		return ErrPhase
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM safe_apply_baseline WHERE transaction_id=?`, transactionID).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrPhase
	}
	for _, file := range files {
		content := file.Content
		if content == nil {
			content = []byte{}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO safe_apply_baseline(transaction_id,template_id,present,content,mode,uid,gid) VALUES(?,?,?,?,?,?,?)`, transactionID, file.TemplateID, file.Present, content, file.Mode, file.UID, file.GID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Baseline fails closed for legacy transactions and incomplete evidence. It
// never reconstructs a historical group using today's system files.
func (store *Store) Baseline(ctx context.Context, transactionID string) ([]BaselineFile, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT template_id,present,content,mode,uid,gid FROM safe_apply_baseline WHERE transaction_id=? ORDER BY template_id`, transactionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []BaselineFile
	for rows.Next() {
		var file BaselineFile
		if err := rows.Scan(&file.TemplateID, &file.Present, &file.Content, &file.Mode, &file.UID, &file.GID); err != nil {
			return nil, ErrBaseline
		}
		if file.Content == nil {
			file.Content = []byte{}
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := validateBaselineGroup(files, true); err != nil {
		return nil, err
	}
	return files, nil
}
