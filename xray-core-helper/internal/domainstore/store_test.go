package domainstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"

	"home-router/xray-core-helper/internal/domain"
)

func TestOpenCreatesEmptySchemaAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "helper.sqlite")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantEmpty := Snapshot{Desired: []string{}, Baseline: []string{}, ConsolidationActive: false}
	if !reflect.DeepEqual(snapshot, wantEmpty) {
		t.Fatalf("empty Snapshot() = %#v; want %#v", snapshot, wantEmpty)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot, err = store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot, wantEmpty) {
		t.Fatalf("reopened empty Snapshot() = %#v; want %#v", snapshot, wantEmpty)
	}
}

func TestDesiredMutationsAreIdempotentAndPreserveBaseline(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.db.Exec("INSERT INTO proxy_domain_state(domain, desired, baseline) VALUES ('base.example', 0, 1)"); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := store.AddDesired(context.Background(), "NEW.Example."); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := store.DeleteDesired(context.Background(), "base.example"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteDesired(context.Background(), "new.example"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDesired(context.Background(), "missing.example"); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{Desired: []string{}, Baseline: []string{"base.example"}, ConsolidationActive: false}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("Snapshot() = %#v; want %#v", snapshot, want)
	}
	if err := store.AddDesired(context.Background(), "not a domain"); err == nil {
		t.Fatal("AddDesired() accepted an invalid domain")
	}
}

func TestActiveConsolidationAllowsSnapshotsAndRejectsMutations(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.AddDesired(context.Background(), "keep.example"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetConsolidationActive(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(context.Context, string) error{store.AddDesired, store.DeleteDesired} {
		if err := mutate(context.Background(), "other.example"); !errors.Is(err, domain.ErrConsolidationActive) {
			t.Fatalf("mutation error = %v; want consolidation conflict", err)
		}
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{Desired: []string{"keep.example"}, Baseline: []string{}, ConsolidationActive: true}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("Snapshot() during consolidation = %#v; want %#v", snapshot, want)
	}
	if err := store.SetConsolidationActive(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := store.AddDesired(context.Background(), "other.example"); err != nil {
		t.Fatalf("mutation after consolidation = %v", err)
	}
}

func TestBeginConsolidationAtomicallyFreezesAndSnapshotsDesiredDomains(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.AddDesired(context.Background(), "B.example."); err != nil {
		t.Fatal(err)
	}
	if err := store.AddDesired(context.Background(), "a.example"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.BeginConsolidation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot, Snapshot{
		Desired: []string{"a.example", "b.example"}, Baseline: []string{}, ConsolidationActive: true,
	}) {
		t.Fatalf("BeginConsolidation() = %#v", snapshot)
	}
	if _, err := store.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot() while frozen = %v", err)
	}
	if err := store.AddDesired(context.Background(), "later.example"); !errors.Is(err, domain.ErrConsolidationActive) {
		t.Fatalf("AddDesired() while frozen = %v; want consolidation conflict", err)
	}
	if _, err := store.BeginConsolidation(context.Background()); !errors.Is(err, domain.ErrConsolidationActive) {
		t.Fatalf("second BeginConsolidation() = %v; want consolidation conflict", err)
	}
}

func TestFinalizeConsolidationAtomicallyCommitsPreparedDesiredSet(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.AddDesired(context.Background(), "B.example."); err != nil {
		t.Fatal(err)
	}
	if err := store.AddDesired(context.Background(), "a.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("INSERT INTO proxy_domain_state(domain, desired, baseline) VALUES ('removed.example', 0, 1)"); err != nil {
		t.Fatal(err)
	}
	prepared, err := store.BeginConsolidation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeConsolidation(context.Background(), prepared.Desired); err != nil {
		t.Fatalf("FinalizeConsolidation() error = %v", err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{
		Desired: []string{"a.example", "b.example"}, Baseline: []string{"a.example", "b.example"},
	}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("Snapshot() after finalization = %#v; want %#v", snapshot, want)
	}
	var obsoleteRows int
	if err := store.db.QueryRow("SELECT count(*) FROM proxy_domain_state WHERE desired = 0 AND baseline = 0").Scan(&obsoleteRows); err != nil {
		t.Fatal(err)
	}
	if obsoleteRows != 0 {
		t.Fatalf("obsolete domain rows remain after finalization: %d", obsoleteRows)
	}
}

func TestFinalizeConsolidationRejectsStalePreparedDomainsWithoutChangingState(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.AddDesired(context.Background(), "prepared.example"); err != nil {
		t.Fatal(err)
	}
	prepared, err := store.BeginConsolidation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("INSERT INTO proxy_domain_state(domain, desired, baseline) VALUES ('later.example', 1, 0)"); err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeConsolidation(context.Background(), prepared.Desired); !errors.Is(err, ErrPreparedDomainsMismatch) {
		t.Fatalf("FinalizeConsolidation() error = %v; want prepared-domain mismatch", err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{Desired: []string{"later.example", "prepared.example"}, Baseline: []string{}, ConsolidationActive: true}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("Snapshot() after stale finalization = %#v; want %#v", snapshot, want)
	}
}

func TestFinalizeConsolidationRequiresActiveState(t *testing.T) {
	for _, missingControl := range []bool{false, true} {
		name := "inactive"
		if missingControl {
			name = "missing control state"
		}
		t.Run(name, func(t *testing.T) {
			store, err := Open(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if err := store.AddDesired(context.Background(), "keep.example"); err != nil {
				t.Fatal(err)
			}
			if missingControl {
				if _, err := store.db.Exec("DELETE FROM proxy_domain_control WHERE singleton = 1"); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.FinalizeConsolidation(context.Background(), []string{"keep.example"}); !errors.Is(err, ErrConsolidationNotActive) {
				t.Fatalf("FinalizeConsolidation() error = %v; want inactive-state error", err)
			}
			var desired, baseline int
			if err := store.db.QueryRow("SELECT desired, baseline FROM proxy_domain_state WHERE domain = 'keep.example'").Scan(&desired, &baseline); err != nil {
				t.Fatal(err)
			}
			if desired != 1 || baseline != 0 {
				t.Fatalf("domain state after rejected finalization = %d/%d; want 1/0", desired, baseline)
			}
		})
	}
}

func TestFinalizeConsolidationDatabaseFailureRollsBackAllChanges(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.AddDesired(context.Background(), "keep.example"); err != nil {
		t.Fatal(err)
	}
	prepared, err := store.BeginConsolidation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_domain_finalize
		BEFORE UPDATE OF baseline ON proxy_domain_state
		BEGIN SELECT RAISE(ABORT, 'injected database failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeConsolidation(context.Background(), prepared.Desired); err == nil {
		t.Fatal("FinalizeConsolidation() succeeded despite injected database failure")
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{Desired: []string{"keep.example"}, Baseline: []string{}, ConsolidationActive: true}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("Snapshot() after failed finalization = %#v; want %#v", snapshot, want)
	}
}

func TestConcurrentDesiredMutationsPreserveSnapshotConsistency(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.db.Exec("INSERT INTO proxy_domain_state(domain, desired, baseline) VALUES ('base.example', 0, 1)"); err != nil {
		t.Fatal(err)
	}

	const writers = 32
	start := make(chan struct{})
	errorsFound := make(chan error, writers+1)
	var wait sync.WaitGroup
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			if err := store.AddDesired(context.Background(), fmt.Sprintf("host-%d.example", index)); err != nil {
				errorsFound <- err
			}
		}(index)
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		<-start
		for index := 0; index < 80; index++ {
			snapshot, err := store.Snapshot(context.Background())
			if err != nil {
				errorsFound <- err
				return
			}
			if !sort.StringsAreSorted(snapshot.Desired) || !reflect.DeepEqual(snapshot.Baseline, []string{"base.example"}) {
				errorsFound <- fmt.Errorf("inconsistent concurrent snapshot: %#v", snapshot)
				return
			}
		}
	}()
	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Desired) != writers || !reflect.DeepEqual(snapshot.Baseline, []string{"base.example"}) {
		t.Fatalf("final Snapshot() = %#v; want %d desired domains and baseline", snapshot, writers)
	}
}

func TestOpenMigratesKnownVersionOneSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		previousSchemaDDL,
		stateDDL,
		"INSERT INTO proxy_domain_schema(singleton, version) VALUES (1, 1)",
		"INSERT INTO proxy_domain_state(domain, desired, baseline) VALUES ('saved.example', 1, 1)",
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{Desired: []string{"saved.example"}, Baseline: []string{"saved.example"}, ConsolidationActive: false}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("Snapshot() after migration = %#v; want %#v", snapshot, want)
	}
}

func TestSnapshotReadsDesiredAndBaselineFromOneDatabaseSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.sqlite")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO proxy_domain_state(domain, desired, baseline) VALUES
		('keep.example', 1, 1), ('removed.example', 0, 1)`); err != nil {
		t.Fatal(err)
	}

	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = writer.Close() })
	writeTx, err := writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writeTx.Rollback()
	if _, err := writeTx.Exec(`UPDATE proxy_domain_state SET desired = 0, baseline = 1
		WHERE domain = 'keep.example'`); err != nil {
		t.Fatal(err)
	}
	if _, err := writeTx.Exec("DELETE FROM proxy_domain_state WHERE domain = 'removed.example'"); err != nil {
		t.Fatal(err)
	}
	if _, err := writeTx.Exec("INSERT INTO proxy_domain_state(domain, desired, baseline) VALUES ('added.example', 1, 0)"); err != nil {
		t.Fatal(err)
	}

	beforeCommit, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantBefore := Snapshot{
		Desired:  []string{"keep.example"},
		Baseline: []string{"keep.example", "removed.example"},
	}
	if !reflect.DeepEqual(beforeCommit, wantBefore) {
		t.Fatalf("Snapshot() during uncommitted update = %#v; want %#v", beforeCommit, wantBefore)
	}
	if err := writeTx.Commit(); err != nil {
		t.Fatal(err)
	}
	afterCommit, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantAfter := Snapshot{
		Desired:  []string{"added.example"},
		Baseline: []string{"keep.example"},
	}
	if !reflect.DeepEqual(afterCommit, wantAfter) {
		t.Fatalf("Snapshot() after committed update = %#v; want %#v", afterCommit, wantAfter)
	}
}

func TestOpenAcceptsExistingHelperTransactionTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE config_state (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			generation INTEGER NOT NULL CHECK (generation >= 0),
			content_hash TEXT NOT NULL
		)`,
		`CREATE TABLE config_transaction (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			transaction_id TEXT NOT NULL UNIQUE,
			phase TEXT NOT NULL,
			previous_generation INTEGER NOT NULL,
			previous_hash TEXT NOT NULL,
			next_hash TEXT NOT NULL,
			candidate_path TEXT NOT NULL,
			backup_path TEXT NOT NULL,
			restart_required INTEGER NOT NULL
		)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsUnknownAndLegacySchemas(t *testing.T) {
	tests := []struct {
		name       string
		statements []string
	}{
		{
			name:       "unknown table",
			statements: []string{"CREATE TABLE unrelated (value TEXT)"},
		},
		{
			name: "legacy domains schema",
			statements: []string{`CREATE TABLE domains (
				domain TEXT PRIMARY KEY,
				desired INTEGER NOT NULL,
				baseline INTEGER NOT NULL
			)`},
		},
		{
			name:       "incomplete proxy-domain schema",
			statements: []string{"CREATE TABLE proxy_domain_state (domain TEXT PRIMARY KEY)"},
		},
		{
			name: "unknown schema version",
			statements: []string{
				schemaDDL,
				stateDDL,
				"PRAGMA ignore_check_constraints = ON",
				"INSERT INTO proxy_domain_schema(singleton, version) VALUES (1, 2)",
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "helper.sqlite")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			for _, statement := range testCase.statements {
				if _, err := db.Exec(statement); err != nil {
					_ = db.Close()
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			store, err := Open(context.Background(), path)
			if err == nil {
				_ = store.Close()
				t.Fatal("Open() accepted unsupported schema")
			}
		})
	}
}

func TestOpenRejectsLegacyTextDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.txt")
	if err := os.WriteFile(path, []byte("example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(context.Background(), path); err == nil {
		_ = store.Close()
		t.Fatal("Open() accepted legacy text storage")
	}
}

func TestOpenRejectsRelativePath(t *testing.T) {
	if store, err := Open(context.Background(), "relative.sqlite"); err == nil {
		_ = store.Close()
		t.Fatal("Open() accepted a relative path")
	} else if err.Error() != "domain database path must be absolute" {
		t.Fatalf("Open() error = %v; want absolute path error", err)
	}
}
