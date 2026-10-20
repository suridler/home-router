package setup

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

// CheckPendingDatabase is read-only and never initializes or clears a database.
// Onboarding must not hide transactions left by an earlier helper installation.
func CheckPendingDatabase(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || !filepath.IsAbs(path) {
		return ErrRecoveryRequired
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return ErrRecoveryRequired
	}
	defer db.Close()
	for _, table := range []struct{ name, condition string }{{"config_transaction", "1=1"}, {"safe_apply_transaction", "state='pending'"}} {
		var exists int
		if db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table.name).Scan(&exists) != nil {
			return ErrRecoveryRequired
		}
		if exists == 0 {
			continue
		}
		var count int
		if db.QueryRowContext(ctx, "SELECT count(*) FROM "+table.name+" WHERE "+table.condition).Scan(&count) != nil || count > 0 {
			return ErrRecoveryRequired
		}
	}
	return nil
}
