package transaction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStorePersistsGenerationAndTransactionJournal(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "helper.sqlite")
	store, err := OpenStore(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Initialize(ctx, "hash-before")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Generation != 0 || metadata.ContentHash != "hash-before" {
		t.Fatalf("initial metadata = %#v", metadata)
	}
	pending := Pending{
		ID:                 "tx-1",
		Phase:              "staged",
		PreviousGeneration: metadata.Generation,
		PreviousHash:       metadata.ContentHash,
		NextHash:           "hash-after",
		CandidatePath:      "/tmp/config.candidate",
		BackupPath:         "/tmp/config.backup",
		RestartRequired:    true,
	}
	if err := store.SavePending(ctx, pending); err != nil {
		t.Fatal(err)
	}
	restoredPending, err := store.Pending(ctx)
	if err != nil || restoredPending == nil || *restoredPending != pending {
		t.Fatalf("pending = %#v, error = %v", restoredPending, err)
	}
	if err := store.Advance(ctx, pending.ID, "staged", "replaced"); err != nil {
		t.Fatal(err)
	}
	if err := store.Advance(ctx, pending.ID, "replaced", "restart_started"); err != nil {
		t.Fatal(err)
	}
	metadata, err = store.CommitPending(ctx, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Generation != 1 || metadata.ContentHash != "hash-after" {
		t.Fatalf("committed metadata = %#v", metadata)
	}
	if journal, err := store.Pending(ctx); err != nil || journal != nil {
		t.Fatalf("pending after commit = %#v, error = %v", journal, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	metadata, err = reopened.Metadata(ctx)
	if err != nil || metadata.Generation != 1 || metadata.ContentHash != "hash-after" {
		t.Fatalf("metadata after reopen = %#v, error = %v", metadata, err)
	}
	info, err := os.Stat(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("database mode = %04o; want 0600", info.Mode().Perm())
	}
}

func TestStoreRejectsStaleGenerationAndConcurrentJournal(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	metadata, err := store.Initialize(ctx, "initial-hash")
	if err != nil {
		t.Fatal(err)
	}
	pending := Pending{
		ID:                 "tx-1",
		Phase:              "staged",
		PreviousGeneration: metadata.Generation,
		PreviousHash:       metadata.ContentHash,
		NextHash:           "candidate-hash",
		CandidatePath:      "/tmp/candidate",
		BackupPath:         "/tmp/backup",
	}
	if err := store.SavePending(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if err := store.SavePending(ctx, pending); !errors.Is(err, ErrTransactionBusy) {
		t.Fatalf("second SavePending() error = %v; want ErrTransactionBusy", err)
	}
	if _, err := store.Adopt(ctx, metadata.Generation, "external-hash"); !errors.Is(err, ErrTransactionBusy) {
		t.Fatalf("Adopt() during pending transaction error = %v; want ErrTransactionBusy", err)
	}
	if err := store.Advance(ctx, pending.ID, "staged", "replaced"); err != nil {
		t.Fatal(err)
	}
	committed, err := store.CommitPending(ctx, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Adopt(ctx, committed.Generation-1, "external-hash"); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("outdated Adopt() error = %v; want ErrStaleGeneration", err)
	}
	adopted, err := store.Adopt(ctx, committed.Generation, "external-hash")
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Generation != 2 || adopted.ContentHash != "external-hash" {
		t.Fatalf("adopted metadata = %#v", adopted)
	}
}

func TestOpenStoreRejectsRelativePath(t *testing.T) {
	if _, err := OpenStore(context.Background(), "relative.sqlite"); err == nil {
		t.Fatal("OpenStore() accepted a relative path")
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreUpgradesV1WithPendingJournal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Initialize(ctx, "original"); err != nil {
		t.Fatal(err)
	}
	pending := Pending{ID: "legacy", Phase: "staged", PreviousHash: "original", NextHash: "next", CandidatePath: "candidate", BackupPath: "backup"}
	if err := store.SavePending(ctx, pending); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"ALTER TABLE config_transaction DROP COLUMN previous_state_hash",
		"DROP TABLE config_rollback",
		"PRAGMA user_version = 1",
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	got, err := upgraded.Pending(ctx)
	if err != nil || got == nil || *got != pending {
		t.Fatalf("pending=%#v err=%v", got, err)
	}
	if got.stateHash() != "original" {
		t.Fatal("lost committed baseline of legacy journal")
	}
	var version int
	if err := upgraded.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("version=%d err=%v", version, err)
	}
}
