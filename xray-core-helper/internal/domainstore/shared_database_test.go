package domainstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/errorhistory"
	"home-router/xray-core-helper/internal/geodataadmin"
	"home-router/xray-core-helper/internal/logreader"
	"home-router/xray-core-helper/internal/safeapply"
	"home-router/xray-core-helper/internal/scheduler"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/transaction"
)

func TestSharedDatabaseReopensAfterSafeApplyInitialization(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "helper.sqlite")
	domains, err := domainstore.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = domains.AddDesired(ctx, "keep.example"); err != nil {
		t.Fatal(err)
	}
	safety, err := safeapply.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := safety.Begin(ctx, templatecatalog.Nftables, "backup", strings.Repeat("a", 64), 3, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = safety.Close(); err != nil {
		t.Fatal(err)
	}
	if err = domains.Close(); err != nil {
		t.Fatal(err)
	}

	// Match daemon startup order on its second and subsequent starts.
	for range 2 {
		domains, err = domainstore.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := domains.Snapshot(ctx)
		if err != nil || len(snapshot.Desired) != 1 || snapshot.Desired[0] != "keep.example" {
			t.Fatalf("domain state lost: %#v, %v", snapshot, err)
		}
		safety, err = safeapply.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := safety.Pending(ctx)
		if err != nil || actual == nil || actual.ID != pending.ID || actual.Phase != pending.Phase {
			t.Fatalf("pending state lost: %#v, %v", actual, err)
		}
		if err = safety.Close(); err != nil {
			t.Fatal(err)
		}
		if err = domains.Close(); err != nil {
			t.Fatal(err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Bypass Begin's own busy check to prove the unique index still protects SQL.
	_, err = db.ExecContext(ctx, `INSERT INTO safe_apply_transaction
		SELECT 'duplicate', template_id, backup_id, candidate_hash, deadline_ns,
		phase, state, generation FROM safe_apply_transaction WHERE state = 'pending'`)
	if err == nil {
		t.Fatal("unique pending index no longer enforced")
	}
	if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("duplicate rejected for an unexpected reason: %v", err)
	}
}

func TestSharedDatabaseStillRejectsUnknownObjects(t *testing.T) {
	for name, statement := range map[string]string{
		"table":   `CREATE TABLE unexpected_table (value TEXT)`,
		"index":   `CREATE INDEX unexpected_index ON safe_apply_transaction(template_id)`,
		"view":    `CREATE VIEW unexpected_view AS SELECT state FROM safe_apply_transaction`,
		"trigger": `CREATE TRIGGER unexpected_trigger AFTER INSERT ON safe_apply_transaction BEGIN SELECT 1; END`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "helper.sqlite")
			safety, err := safeapply.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if err = safety.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := domainstore.Open(ctx, path)
			if err == nil {
				store.Close()
				t.Fatal("unknown schema object accepted")
			}
			if !strings.Contains(err.Error(), "unexpected_"+name) {
				t.Fatalf("wrong rejection: %v", err)
			}
		})
	}
}

type restartLogReader struct{}

func (restartLogReader) ReadHistory(context.Context, time.Time) (logreader.Result, error) {
	return logreader.Result{Source: "journal", Events: []logreader.Event{{
		ID: "restart-error", Time: time.Unix(100, 0), Source: "journal", Message: "[Error] connection failed",
	}}}, nil
}

// Exercise the combined database in daemon startup order, rather than checking
// each component against its own isolated database. Both a healthy restart and
// a restart with pending rollback evidence must preserve every component's state.
func TestAllSharedStoresReopenWithScheduler(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "pending"}[pending], func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "helper.sqlite")
			due := time.Unix(1700000000, 0)
			var pendingID string
			for cycle := range 3 {
				func() {
					transactions, err := transaction.OpenStore(ctx, path)
					if err != nil {
						t.Fatal(cycle, err)
					}
					defer transactions.Close()
					domains, err := domainstore.Open(ctx, path)
					if err != nil {
						t.Fatal(cycle, err)
					}
					defer domains.Close()
					geodata, err := geodataadmin.Open(ctx, path, bootstrap.Defaults().Geodata)
					if err != nil {
						t.Fatal(cycle, err)
					}
					defer geodata.Close()
					safety, err := safeapply.Open(ctx, path)
					if err != nil {
						t.Fatal(cycle, err)
					}
					defer safety.Close()
					history, err := errorhistory.Open(ctx, path, restartLogReader{})
					if err != nil {
						t.Fatal(cycle, err)
					}
					defer history.Close()
					schedules, err := scheduler.Open(ctx, path)
					if err != nil {
						t.Fatal(cycle, err)
					}
					defer schedules.Close()
					if cycle == 0 {
						if err := domains.AddDesired(ctx, "keep.example"); err != nil {
							t.Fatal(err)
						}
						if _, err := domains.BeginConsolidation(ctx); err != nil {
							t.Fatal(err)
						}
						if err := domains.FinalizeConsolidation(ctx, []string{"keep.example"}); err != nil {
							t.Fatal(err)
						}
						if err := domains.AddDesired(ctx, "dynamic.example"); err != nil {
							t.Fatal(err)
						}
						if pending {
							operation, err := safety.Begin(ctx, templatecatalog.Nftables, "backup", strings.Repeat("a", 64), 3, time.Now().Add(time.Minute))
							if err != nil {
								t.Fatal(err)
							}
							pendingID = operation.ID
						}
					}
					snapshot, err := domains.Snapshot(ctx)
					if err != nil || len(snapshot.Desired) != 2 || len(snapshot.Baseline) != 1 || snapshot.Baseline[0] != "keep.example" || snapshot.ConsolidationActive {
						t.Fatal("lost domain state", cycle, snapshot, err)
					}
					operation, err := safety.Pending(ctx)
					if err != nil || (operation != nil) != pending {
						t.Fatal("lost recovery state", cycle, operation, err)
					}
					if pending && operation.ID != pendingID {
						t.Fatal("pending operation changed")
					}
					if err := history.Collect(ctx); err != nil {
						t.Fatal(err)
					}
					recent, err := history.List(ctx, 100)
					if err != nil || len(recent.Errors) != 1 {
						t.Fatal("error history lost or duplicated", cycle, recent, err)
					}
					job := schedules.Persist(scheduler.GeodataJob("unused", 4, 0), "geodata-04:00")
					initial := due
					if cycle != 0 {
						initial = due.Add(24 * time.Hour)
					}
					actual, err := job.Pending(ctx, initial)
					if err != nil || !actual.Equal(due) {
						t.Fatal("missed deadline lost", cycle, actual, err)
					}
				}()
			}
		})
	}
}
