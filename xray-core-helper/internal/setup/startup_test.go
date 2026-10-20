package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"home-router/xray-core-helper/internal/templatecatalog"
)

func TestStartupRecoversBeforeProbeAfterEveryReplacement(t *testing.T) {
	for _, original := range []struct {
		name     string
		contents []byte
		absent   bool
		state    ConfigState
		setup    bool
	}{
		{"absent", nil, true, ConfigAbsent, true},
		{"empty", nil, false, ConfigEmpty, true},
		{"whitespace", []byte(" \n\t"), false, ConfigWhitespace, true},
		{"empty-object", []byte("{}"), false, ConfigEmptyObject, true},
		{"existing", []byte(`{"log":{"loglevel":"warning"}}`), false, ConfigExisting, true},
	} {
		for replaced := 0; replaced <= len(templatecatalog.Default().Entries()); replaced++ {
			t.Run(fmt.Sprintf("%s/after-%d-files", original.name, replaced), func(t *testing.T) {
				store, entries, _ := fileStoreFixture(t)
				config := entries[0].SystemPath
				if original.absent {
					if err := os.Remove(config); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(config, original.contents, 0640); err != nil {
					t.Fatal(err)
				}
				tx, err := store.Prepare(context.Background(), Plan{Interface: "lan0", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24"})
				if err != nil {
					t.Fatal(err)
				}
				if err = tx.Validate(context.Background()); err != nil {
					t.Fatal(err)
				}
				// Simulate process loss after an arbitrary prefix of committed files:
				// do not run the transaction's in-process rollback.
				for _, item := range tx.(*fileTransaction).manifest.Items[:replaced] {
					raw, err := os.ReadFile(item.Candidate)
					if err != nil {
						t.Fatal(err)
					}
					if err = atomicReplace(item.Target, raw, os.FileMode(item.Mode)); err != nil {
						t.Fatal(err)
					}
				}
				reopened, err := newFileStore(store.root, entries, store.validate)
				if err != nil {
					t.Fatal(err)
				}
				for attempt := 0; attempt < 2; attempt++ {
					startup, err := NewStartup(context.Background(), config, reopened)
					if err != nil {
						t.Fatal(err)
					}
					got := startup.Status()
					if got.RecoveryRequired || got.State != original.state || got.SetupRequired != original.setup {
						t.Fatalf("startup=%#v, expected restored %s", got, original.state)
					}
				}
				if original.absent {
					if _, err := os.Stat(config); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("config err=%v", err)
					}
				} else {
					raw, err := os.ReadFile(config)
					if err != nil || string(raw) != string(original.contents) {
						t.Fatalf("original bytes not restored: %v", err)
					}
				}
				for _, entry := range entries[1:] {
					if _, err := os.Stat(entry.SystemPath); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("candidate remains for %s: %v", entry.ID, err)
					}
				}
				if _, err := os.Stat(store.pendingPath()); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("pending manifest err=%v", err)
				}
			})
		}
	}
}

func TestStartupFailureBlocksApplyUntilExplicitRecoverySucceeds(t *testing.T) {
	store, entries, _ := fileStoreFixture(t)
	tx, err := store.Prepare(context.Background(), Plan{Interface: "lan0", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	backup := tx.(*fileTransaction).manifest.Items[0].Backup
	if err = os.Remove(backup); err != nil {
		t.Fatal(err)
	}
	startup, err := NewStartup(context.Background(), entries[0].SystemPath, store)
	if err != nil {
		t.Fatal(err)
	}
	if got := startup.Status(); !got.RecoveryRequired || !got.SetupRequired || got.State != ConfigUnavailable {
		t.Fatalf("partial non-empty config admitted: %#v", got)
	}
	if _, err = os.Stat(store.pendingPath()); err != nil {
		t.Fatalf("recovery evidence lost: %v", err)
	}
	calls := []string{}
	engine, err := NewEngine(startup.Status(), discoveryFake{[]Interface{{Name: "lan0", Addresses: []string{"192.0.2.1/24"}}}}, store, starterFake{calls: &calls})
	if err != nil {
		t.Fatal(err)
	}
	service := NewStartupService(startup, nil, engine)
	for attempt := 0; attempt < 2; attempt++ {
		if _, err = service.Plan(context.Background(), "lan0"); !errors.Is(err, ErrRecoveryRequired) {
			t.Fatalf("plan err=%v", err)
		}
		if _, err = service.Apply(context.Background(), Plan{}); !errors.Is(err, ErrRecoveryRequired) {
			t.Fatalf("apply err=%v", err)
		}
		if err = service.Recover(context.Background()); !errors.Is(err, ErrRecoveryRequired) {
			t.Fatalf("recover err=%v", err)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("runtime called while recovery blocked: %v", calls)
	}
	if err = os.WriteFile(backup, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := service.Status(); got.RecoveryRequired || got.State != ConfigEmpty || !got.SetupRequired {
		t.Fatalf("status=%#v", got)
	}
	if _, err = service.Plan(context.Background(), "lan0"); err != nil {
		t.Fatalf("plan after recovery: %v", err)
	}
	if err = service.Recover(context.Background()); err != nil {
		t.Fatalf("repeat recovery: %v", err)
	}
}

func TestStartupFailsClosedOnInvalidManifestAndUnreadableConfig(t *testing.T) {
	for _, invalidManifest := range []bool{true, false} {
		t.Run(fmt.Sprint(invalidManifest), func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(root, "config.json")
			store, err := newFileStore(filepath.Join(root, "state"), []templatecatalog.Entry{{ID: templatecatalog.XrayConfig}}, func(context.Context, templatecatalog.ID, string) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if invalidManifest {
				if err = os.MkdirAll(store.root, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(store.pendingPath(), []byte("secret invalid manifest"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err = os.Mkdir(config, 0700); err != nil {
				t.Fatal(err)
			}
			startup, err := NewStartup(context.Background(), config, store)
			if err != nil || !startup.Status().RecoveryRequired {
				t.Fatalf("startup=%#v err=%v", startup, err)
			}
		})
	}
}

func TestStartupServiceStatusAndRecoveryAreSerialized(t *testing.T) {
	calls := []string{}
	store := storeFake{calls: &calls}
	startup, err := NewStartup(context.Background(), filepath.Join(t.TempDir(), "missing"), store)
	if err != nil {
		t.Fatal(err)
	}
	service := NewStartupService(startup, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = service.Status()
				if err := service.Recover(context.Background()); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
}
