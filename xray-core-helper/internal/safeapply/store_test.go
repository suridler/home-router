package safeapply

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/templatecatalog"
)

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func TestStoreReopenPreservesEveryPendingPhase(t *testing.T) {
	for _, phase := range []Phase{PhasePrepared, PhaseApplied, PhaseConfirming, PhaseRollingBack} {
		t.Run(string(phase), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.sqlite")
			store := openTestStore(t, path)
			store.now = func() time.Time { return time.Unix(100, 0) }
			transaction, err := store.Begin(context.Background(), templatecatalog.Nftables, "backup", strings.Repeat("a", 64), 7, time.Unix(220, 0))
			if err != nil {
				t.Fatal(err)
			}
			if phase != PhasePrepared {
				transaction, err = store.Advance(context.Background(), transaction.ID, PhasePrepared, PhaseApplied)
				if err != nil {
					t.Fatal(err)
				}
			}
			if phase == PhaseConfirming {
				transaction, err = store.Advance(context.Background(), transaction.ID, PhaseApplied, PhaseConfirming)
			} else if phase == PhaseRollingBack {
				transaction, err = store.Advance(context.Background(), transaction.ID, PhaseApplied, PhaseRollingBack)
			}
			if err != nil {
				t.Fatal(err)
			}
			_ = store.Close()
			store = openTestStore(t, path)
			defer store.Close()
			pending, err := store.Pending(context.Background())
			if err != nil || pending == nil || pending.Phase != phase || pending.BackupID != "backup" || pending.Generation != 7 {
				t.Fatalf("pending=%#v err=%v", pending, err)
			}
		})
	}
}
func TestStoreAllowsOnlyOnePendingAndRejectsStaleCompletion(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer store.Close()
	store.now = func() time.Time { return time.Unix(100, 0) }
	first, err := store.Begin(context.Background(), templatecatalog.XrayConfig, "one", strings.Repeat("a", 64), 1, time.Unix(220, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Begin(context.Background(), templatecatalog.Sysctl, "two", strings.Repeat("b", 64), 1, time.Unix(220, 0)); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy err=%v", err)
	}
	resolved, changed, err := store.Resolve(context.Background(), first.ID, StateRolledBack)
	if err != nil || !changed || resolved.State != StateRolledBack {
		t.Fatalf("resolved=%#v changed=%v err=%v", resolved, changed, err)
	}
	if _, changed, err = store.Resolve(context.Background(), first.ID, StateRolledBack); err != nil || changed {
		t.Fatalf("idempotent changed=%v err=%v", changed, err)
	}
	second, err := store.Begin(context.Background(), templatecatalog.Sysctl, "two", strings.Repeat("b", 64), 2, time.Unix(220, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Resolve(context.Background(), first.ID, StateRolledBack); !errors.Is(err, ErrStale) {
		t.Fatalf("stale err=%v second=%s", err, second.ID)
	}
}
