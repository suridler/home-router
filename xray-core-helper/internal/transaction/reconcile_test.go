package transaction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateAutomaticallyAdoptsValidExternalConfig(t *testing.T) {
	validator := &fakeValidator{}
	restarter := &fakeRestarter{}
	engine, store, path := newTestEngine(t, transactionFixture, validator, restarter)
	external := []byte(transactionFixture + "\n// external edit\n")
	if err := os.WriteFile(path, external, 0640); err != nil {
		t.Fatal(err)
	}

	if err := engine.ReconcileDrift(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Metadata(context.Background())
	if state.Generation != 1 || state.ContentHash != hashBytes(external) {
		t.Fatalf("state=%#v", state)
	}
	if err := engine.ReconcileDrift(context.Background()); err != nil {
		t.Fatal(err)
	}
	if validator.count() != 1 || restarter.count() != 0 {
		t.Fatalf("validation=%d restart=%d", validator.count(), restarter.count())
	}

	current, _ := os.ReadFile(path)
	if string(current) != string(external) {
		t.Fatal("adoption rewrote config")
	}
}

func TestStateRetainsDriftWhenExternalValidationFails(t *testing.T) {
	for _, failure := range []error{ErrValidationFailed, ErrMarkupInvalid} {
		t.Run(failure.Error(), func(t *testing.T) {
			validator := &fakeValidator{err: failure}
			engine, store, path := newTestEngine(t, transactionFixture, validator, nil)
			external := []byte(transactionFixture + "\n// external edit")
			if err := os.WriteFile(path, external, 0640); err != nil {
				t.Fatal(err)
			}

			if err := engine.ReconcileDrift(context.Background()); !errors.Is(err, failure) {
				t.Fatalf("reconcile=%v", err)
			}

			metadata, _ := store.Metadata(context.Background())
			if metadata.ContentHash != hashBytes([]byte(transactionFixture)) {
				t.Fatal("failed validation adopted file")
			}
			if _, err := engine.Apply(context.Background(), 0, loglevelEdit(external, "debug"), false); !errors.Is(err, ErrStateDrift) {
				t.Fatalf("write not blocked: %v", err)
			}
		})
	}
}

func TestReconcileDoesNotAdoptChangesDuringValidation(t *testing.T) {
	validator := &fakeValidator{}
	engine, store, path := newTestEngine(t, transactionFixture, validator, nil)
	if err := os.WriteFile(path, []byte(transactionFixture+"\n// first"), 0640); err != nil {
		t.Fatal(err)
	}
	validator.fn = func(string) error { return os.WriteFile(path, []byte(transactionFixture+"\n// second"), 0640) }
	if err := engine.ReconcileDrift(context.Background()); !errors.Is(err, ErrStateDrift) {
		t.Fatalf("reconcile=%v", err)
	}
	metadata, _ := store.Metadata(context.Background())
	if metadata.Generation != 0 {
		t.Fatal("adopted unvalidated edit")
	}
}

func TestReconcileLeavesPendingTransactionAlone(t *testing.T) {
	validator := &fakeValidator{}
	engine, store, path := newTestEngine(t, transactionFixture, validator, nil)
	before, _ := store.Metadata(context.Background())
	if err := store.SavePending(context.Background(), Pending{ID: "pending", Phase: "staged", PreviousHash: before.ContentHash, NextHash: "candidate", CandidatePath: path + ".candidate", BackupPath: path + ".backup"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(transactionFixture+"\n// external"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReconcileDrift(context.Background()); !errors.Is(err, ErrTransactionBusy) {
		t.Fatalf("reconcile=%v", err)
	}
	if validator.count() != 0 {
		t.Fatal("validated pending transaction")
	}
}

func TestXrayValidatorChecksHelperMarkupBeforeCommand(t *testing.T) {
	dir := t.TempDir()
	// The executable would succeed, proving malformed annotations must be
	// rejected by helper independently of Xray's treatment of comments.
	binary := filepath.Join(dir, "xray")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	validator, err := NewXrayValidator(binary, dir)
	if err != nil {
		t.Fatal(err)
	}
	valid := "{\n// helper:v1 bind level type=log-level source=helper.logging.level token=\"\\\"warning\\\"\"\n\"loglevel\": \"warning\"\n}"
	for _, tc := range []struct {
		name, source string
		invalid      bool
	}{
		{"valid", valid, false},
		{"wrong token", strings.Replace(valid, "\"loglevel\": \"warning\"", "\"loglevel\": \"info\"", 1), true},
		{"duplicate", strings.Replace(valid, "\n}", ",\n"+strings.SplitN(valid, "\n", 2)[1], 1), true},
		{"unknown provider", strings.Replace(valid, "helper.logging.level", "arbitrary.provider", 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			err := validator.Validate(context.Background(), path)
			if tc.invalid && !errors.Is(err, ErrMarkupInvalid) {
				t.Fatalf("validation=%v", err)
			}
			if !tc.invalid && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRollbackRestoresPreviousAdoptionAndRestarts(t *testing.T) {
	validator := &fakeValidator{}
	restarter := &fakeRestarter{}
	engine, store, path := newTestEngine(t, transactionFixture, validator, restarter)
	external := []byte(transactionFixture + "\n// external version")
	if err := os.WriteFile(path, external, 0640); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReconcileDrift(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := engine.RollbackStatus(context.Background())
	if err != nil || !status.Available || status.Generation != 1 {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	if _, err := engine.Rollback(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != transactionFixture || restarter.count() != 1 {
		t.Fatal("rollback did not restore and restart")
	}
	metadata, _ := store.Metadata(context.Background())
	if metadata.Generation != 2 {
		t.Fatal("rollback did not commit new generation")
	}
}

func TestRollbackRestoresUnadoptedInvalidExternalEdit(t *testing.T) {
	validator := &fakeValidator{}
	restarter := &fakeRestarter{}
	engine, store, path := newTestEngine(t, transactionFixture, validator, restarter)
	external := []byte("invalid externally modified config")
	if err := os.WriteFile(path, external, 0640); err != nil {
		t.Fatal(err)
	}
	validator.err = ErrValidationFailed
	if err := engine.ReconcileDrift(context.Background()); !errors.Is(err, ErrValidationFailed) {
		t.Fatal(err)
	}
	// Only the external file is invalid. The saved baseline is still valid.
	validator.err = nil
	if _, err := engine.Rollback(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != transactionFixture || restarter.count() != 1 {
		t.Fatal("rollback did not repair invalid external edit")
	}
	status, err := engine.RollbackStatus(context.Background())
	if err != nil || status.Available {
		t.Fatalf("invalid external config offered as rollback: %#v %v", status, err)
	}
	metadata, _ := store.Metadata(context.Background())
	if metadata.ContentHash != hashBytes([]byte(transactionFixture)) || metadata.Generation != 1 {
		t.Fatalf("metadata=%#v", metadata)
	}
}

func TestRollbackRejectsInvalidSavedVersionAndPreservesCurrent(t *testing.T) {
	validator := &fakeValidator{}
	restarter := &fakeRestarter{}
	engine, _, path := newTestEngine(t, transactionFixture, validator, restarter)
	external := []byte(transactionFixture + "\n// external")
	if err := os.WriteFile(path, external, 0640); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReconcileDrift(context.Background()); err != nil {
		t.Fatal(err)
	}
	validator.err = ErrMarkupInvalid
	if _, err := engine.Rollback(context.Background(), 1); !errors.Is(err, ErrMarkupInvalid) {
		t.Fatal(err)
	}
	current, _ := os.ReadFile(path)
	if string(current) != string(external) || restarter.count() != 0 {
		t.Fatal("invalid rollback changed current config")
	}
}

func TestHistoryInitializesOnlyKnownCommittedContent(t *testing.T) {
	engine, _, path := newTestEngine(t, transactionFixture, &fakeValidator{}, nil)
	baseline := engine.acceptedPath(hashBytes([]byte(transactionFixture)))
	if _, err := os.Stat(baseline); err != nil {
		t.Fatal("baseline not initialized", err)
	}
	if err := os.Remove(baseline); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(transactionFixture+"\n// unknown"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := engine.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(baseline); !os.IsNotExist(err) {
		t.Fatal("invented previous version")
	}
	if err := engine.ReconcileDrift(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := engine.RollbackStatus(context.Background())
	if err != nil || status.Available {
		t.Fatalf("unavailable history=%#v %v", status, err)
	}
}

// A restart failure after replacing unadopted external content must retain
// both its actual pre-operation hash and the different committed DB baseline.
func TestRollbackOfExternalDriftRemainsRecoverable(t *testing.T) {
	restarter := &fakeRestarter{errs: []error{errors.New("restart failed"), errors.New("recovery restart failed")}}
	engine, store, path := newTestEngine(t, transactionFixture, &fakeValidator{}, restarter)
	external := []byte(transactionFixture + "\n// unadopted")
	if err := os.WriteFile(path, external, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Rollback(context.Background(), 0); !errors.Is(err, ErrRollbackFailed) {
		t.Fatal(err)
	}
	pending, err := store.Pending(context.Background())
	if err != nil || pending == nil || pending.PreviousStateHash == pending.PreviousHash {
		t.Fatalf("journal=%#v %v", pending, err)
	}
	if err := engine.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _ := os.ReadFile(path)
	if string(current) != string(external) {
		t.Fatal("recovery overwrote pre-operation external config")
	}
}

func TestHistoryKeepsOnlyCurrentAndPreviousAcceptedVersions(t *testing.T) {
	engine, _, path := newTestEngine(t, transactionFixture, &fakeValidator{}, nil)
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(path, []byte(transactionFixture+strings.Repeat("\n", i+1)), 0640); err != nil {
			t.Fatal(err)
		}
		if err := engine.ReconcileDrift(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(engine.acceptedPath(""))
	if err != nil || len(entries) != 2 {
		t.Fatalf("history entries=%d err=%v", len(entries), err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatal("snapshot is not private")
		}
	}
}
