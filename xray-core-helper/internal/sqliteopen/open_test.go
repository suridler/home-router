package sqliteopen

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestConnectionPolicySurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.sqlite")
	for range 2 {
		db, err := Open(context.Background(), path, Options{BusyTimeout: 5 * time.Second, WAL: true, ForeignKeys: true})
		if err != nil {
			t.Fatal(err)
		}
		for pragma, want := range map[string]int{"busy_timeout": 5000, "foreign_keys": 1, "synchronous": 2} {
			var actual int
			if err := db.QueryRow("PRAGMA " + pragma).Scan(&actual); err != nil || actual != want {
				t.Fatalf("%s=%d, %v", pragma, actual, err)
			}
		}
		var mode string
		if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal_mode=%s %v", mode, err)
		}
		if _, err := db.Exec("CREATE TABLE IF NOT EXISTS owned (value TEXT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCanceledOpenDoesNotReturnConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "helper.sqlite"), Options{})
	if err == nil || db != nil {
		t.Fatalf("db=%v error=%v", db, err)
	}
}
