package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/templateapply"
)

func setupLifecycleFixture(t *testing.T) *fileStore {
	t.Helper()
	s, _, root := fileStoreFixture(t)
	for _, asset := range templateapply.DefaultLifecycleAssets() {
		asset.Source = filepath.Join(root, "source", string(asset.ID))
		asset.Target = filepath.Join(root, "native", string(asset.ID))
		asset.UID, asset.GID = os.Geteuid(), os.Getegid()
		if err := os.MkdirAll(filepath.Dir(asset.Source), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(asset.Source, []byte("fixed native asset "+string(asset.ID)), 0600); err != nil {
			t.Fatal(err)
		}
		s.lifecycleAssets = append(s.lifecycleAssets, asset)
	}
	return s
}

func TestSetupLifecycleFilesParticipateInCommitAndRecovery(t *testing.T) {
	s := setupLifecycleFixture(t)
	ctx := context.Background()
	raw, err := s.Prepare(ctx, Plan{Interface: "lan0", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	tx := raw.(*fileTransaction)
	if len(tx.manifest.Lifecycle) != 3 || tx.manifest.LifecycleVersion != 1 {
		t.Fatal("missing lifecycle evidence")
	}
	if err = tx.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, asset := range s.lifecycleAssets {
		if _, err = os.Stat(asset.Target); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	for _, asset := range s.lifecycleAssets {
		if _, err = os.Stat(asset.Target); !os.IsNotExist(err) {
			t.Fatal("prior absence not restored")
		}
	}
}

func TestSetupLifecycleDriftAndMappingAreRejected(t *testing.T) {
	s := setupLifecycleFixture(t)
	ctx := context.Background()
	raw, err := s.Prepare(ctx, Plan{Interface: "lan0", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	tx := raw.(*fileTransaction)
	if err = tx.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	last := s.lifecycleAssets[2].Target
	if err = os.MkdirAll(filepath.Dir(last), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(last, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err == nil {
		t.Fatal("external edit overwritten")
	}
	if _, err = os.Stat(s.lifecycleAssets[0].Target); !os.IsNotExist(err) {
		t.Fatal("partial commit before drift rejection")
	}
	saved := tx.manifest.Lifecycle
	tx.manifest.Lifecycle = nil
	if err = s.validateManifest(tx.manifest); err == nil {
		t.Fatal("missing lifecycle evidence accepted")
	}
	tx.manifest.Lifecycle = saved
	tx.manifest.Lifecycle[0].Target = filepath.Join(t.TempDir(), "escape")
	if err = s.validateManifest(tx.manifest); err == nil {
		t.Fatal("unallowlisted recovery destination")
	}
}

func TestSetupReloadsNativeUnitsAfterCommitAndRestore(t *testing.T) {
	s := setupLifecycleFixture(t)
	reloads := 0
	s.reloadNative = func(context.Context) error {
		reloads++
		for _, asset := range s.lifecycleAssets {
			_, err := os.Stat(asset.Target)
			if reloads == 1 && err != nil {
				t.Fatal("reload before installation", err)
			}
			if reloads == 2 && !os.IsNotExist(err) {
				t.Fatal("reload before restoring prior absence", err)
			}
		}
		return nil
	}
	tx, err := s.Prepare(context.Background(), Plan{Interface: "lan0", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reloads != 2 {
		t.Fatal(reloads)
	}
}
