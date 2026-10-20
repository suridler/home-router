package setup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestOnboardingRefusesPendingDatabaseWithoutClearingIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	if CheckPendingDatabase(ctx, path) != nil {
		t.Fatal("absent database rejected")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("read check created database")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE safe_apply_transaction (state TEXT); INSERT INTO safe_apply_transaction VALUES ('pending')"); err != nil {
		t.Fatal(err)
	}
	if CheckPendingDatabase(ctx, path) != ErrRecoveryRequired {
		t.Fatal("pending ignored")
	}
	var count int
	_ = db.QueryRow("SELECT count(*) FROM safe_apply_transaction").Scan(&count)
	if count != 1 {
		t.Fatal("evidence cleared")
	}
	if _, err = db.Exec("UPDATE safe_apply_transaction SET state='confirmed'"); err != nil {
		t.Fatal(err)
	}
	if err = CheckPendingDatabase(ctx, path); err != nil {
		t.Fatal(err)
	}
}
