package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/templatecatalog"
)

func geositeStoreFixture(t *testing.T, existing bool) (*fileStore, Plan, []templatecatalog.Entry) {
	t.Helper()
	s, entries, root := fileStoreFixture(t)
	for _, entry := range entries {
		names := map[templatecatalog.ID]string{templatecatalog.XrayConfig: "xray/config.json", templatecatalog.Nftables: "nftables/nftables.conf", templatecatalog.TProxyUnit: "systemd/xray-tproxy-routing.service", templatecatalog.Sysctl: "sysctl/99-xray.conf"}
		raw, err := os.ReadFile("../../templConf/" + names[entry.ID])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(entry.WorkingPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s.assetDir = filepath.Join(root, "assets")
	if err := os.Mkdir(s.assetDir, 0700); err != nil {
		t.Fatal(err)
	}
	builder := filepath.Join(root, "builder")
	// Fixture only: emulate the builder's fixed output arguments, never host data.
	if err := os.WriteFile(builder, []byte("#!/bin/sh\nprintf 'fixture USERLIST' > \"$6/$4\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	s.geositeBuilder = builder
	if existing {
		if err := os.WriteFile(s.geositePath(), []byte("existing USERLIST"), 0600); err != nil {
			t.Fatal(err)
		}
		s.geositeBuilder = filepath.Join(root, "missing-builder")
	}
	if err := os.WriteFile(filepath.Join(s.assetDir, "geosite.dat"), []byte("geosite fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.assetDir, "geoip.dat"), []byte("geoip fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	s.validateAssets = func(ctx context.Context, config, assets string) error {
		// Verify validation sees a private projected asset directory, never live writes.
		if assets == s.assetDir {
			t.Fatal("live assets used during staged validation")
		}
		raw, err := os.ReadFile(filepath.Join(assets, s.userGeosite))
		if err != nil || !strings.Contains(string(raw), "USERLIST") {
			t.Fatal("user asset unavailable to Xray validation", err)
		}
		if _, err := os.ReadFile(filepath.Join(assets, "geoip.dat")); err != nil {
			t.Fatal("companion asset missing", err)
		}
		return s.validate(ctx, templatecatalog.XrayConfig, config)
	}
	s.checkRelations = true
	s.identity = func(context.Context) (int, error) { return os.Getegid(), nil }
	plan, err := s.AnalyzeGateway(context.Background(), Plan{Interface: "lan0", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	return s, plan, entries
}

func TestSetupGeositeCreationAndRecovery(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "preserve"}[existing], func(t *testing.T) {
			s, plan, entries := geositeStoreFixture(t, existing)
			tx, err := s.Prepare(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Validate(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !existing {
				if _, err := os.Stat(s.geositePath()); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("asset installed before commit")
				}
			}
			if err := tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(s.geositePath())
			if err != nil {
				t.Fatal(err)
			}
			if existing && string(raw) != "existing USERLIST" {
				t.Fatal("existing asset replaced")
			}
			config, err := os.ReadFile(entries[0].SystemPath)
			if err != nil || !strings.Contains(string(config), "ext:"+s.userGeosite+":USERLIST") {
				t.Fatal("rule missing", err)
			}
			if err := s.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if existing {
				restored, err := os.ReadFile(s.geositePath())
				if err != nil || string(restored) != string(raw) {
					t.Fatal("existing asset altered by recovery")
				}
			} else if _, err := os.Stat(s.geositePath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("new asset not removed during recovery")
			}
		})
	}
}

func TestSetupGeositeFailureLeavesLiveFilesUnchanged(t *testing.T) {
	s, plan, entries := geositeStoreFixture(t, false)
	tx, err := s.Prepare(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	s.validateAssets = func(context.Context, string, string) error { return errors.New("fixture invalid candidate") }
	if err := tx.Validate(context.Background()); err == nil {
		t.Fatal("validation failure ignored")
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(entries[0].SystemPath)
	if err != nil || len(raw) != 0 {
		t.Fatal("system config changed")
	}
	if _, err := os.Stat(s.geositePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed validation installed asset")
	}
	// Count fixture validation without invoking any external validator.
	s.validate(context.Background(), entries[0].ID, entries[0].WorkingPath)
}

func TestSetupGeositeRejectsDriftAndKeepsRecoveryEvidence(t *testing.T) {
	s, plan, entries := geositeStoreFixture(t, false)
	tx, err := s.Prepare(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.geositePath(), []byte("external data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(context.Background()); err == nil {
		t.Fatal("drift ignored")
	}
	if _, err := os.Stat(s.pendingPath()); err != nil {
		t.Fatal("evidence cleared")
	}
	raw, err := os.ReadFile(entries[0].SystemPath)
	if err != nil || len(raw) == 0 {
		t.Fatal("partial config rollback before drift detection")
	}
}

func TestSetupGeositeRejectsConfigChangeDuringRecovery(t *testing.T) {
	s, plan, entries := geositeStoreFixture(t, false)
	tx, err := s.Prepare(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.userGeosite = "geosite_other.dat"
	if err := s.Recover(context.Background()); err == nil {
		t.Fatal("changed setting accepted for recovery")
	}
	if _, err := os.Stat(s.pendingPath()); err != nil {
		t.Fatal("evidence cleared")
	}
	if err := tx.Rollback(context.Background()); err == nil {
		t.Fatal("rollback with mismatched setting accepted")
	}
	// The original manifest is still recoverable with its bound settings.
	s.userGeosite = "geosite_user.dat"
	if err := s.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.validate(context.Background(), entries[0].ID, entries[0].WorkingPath)
}

func TestSetupGeositePublishesNoFilesWhenBuilderFails(t *testing.T) {
	s, plan, entries := geositeStoreFixture(t, false)
	s.geositeBuilder = filepath.Join(s.assetDir, "missing-builder")
	if _, err := s.Prepare(context.Background(), plan); err == nil {
		t.Fatal("missing builder ignored")
	}
	if _, err := os.Stat(s.geositePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("asset written")
	}
	if _, err := os.Stat(s.pendingPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete preparation published manifest")
	}
	raw, err := os.ReadFile(entries[0].SystemPath)
	if err != nil || len(raw) != 0 {
		t.Fatal("config written")
	}
	s.validate(context.Background(), entries[0].ID, entries[0].WorkingPath)
}

func TestSetupGeositeWithRealBuilder(t *testing.T) {
	builder := os.Getenv("XRAY_HELPER_TEST_GEODATA_BUILDER")
	if builder == "" {
		t.Skip("set XRAY_HELPER_TEST_GEODATA_BUILDER for compiler integration")
	}
	if !filepath.IsAbs(builder) {
		t.Fatal("builder fixture must use an absolute path")
	}
	s, plan, _ := geositeStoreFixture(t, false)
	s.geositeBuilder = builder
	tx, err := s.Prepare(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.geositePath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "USERLIST") || !strings.Contains(string(raw), "xray-core-helper-empty-domain.invalid") {
		t.Fatal("compiled USERLIST missing")
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
}
