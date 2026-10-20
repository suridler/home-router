package setup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/templatecatalog"
)

var emptyGeoipFixture = []byte{0x0a, 0x0a, 0x0a, 0x08, 'U', 'S', 'E', 'R', 'L', 'I', 'S', 'T'}

func geoipStoreFixture(t *testing.T, existing bool) (*fileStore, Plan, []templatecatalog.Entry) {
	t.Helper()
	s, _, entries := geositeStoreFixture(t, false)
	s.userGeoip = "geoip_custom.dat"
	draft, err := os.ReadFile("../../templConf/xray/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entries[0].WorkingPath, draft, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.assetDir, "geosite.dat"), []byte("fixture main geosite"), 0600); err != nil {
		t.Fatal(err)
	}
	if existing {
		if err := os.WriteFile(filepath.Join(s.assetDir, s.userGeoip), emptyGeoipFixture, 0640); err != nil {
			t.Fatal(err)
		}
	}
	baseValidator := s.validateAssets
	s.validateAssets = func(ctx context.Context, config, assets string) error {
		data, err := os.ReadFile(filepath.Join(assets, s.userGeoip))
		if err != nil || !bytes.Equal(data, emptyGeoipFixture) {
			t.Fatal("geoip category missing from private validation projection", err)
		}
		return baseValidator(ctx, config, assets)
	}
	plan, err := s.AnalyzeGateway(context.Background(), Plan{Interface: "lan0", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	return s, plan, entries
}

func TestSetupGeoipPairCreationPreservationAndRecovery(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "preserve"}[existing], func(t *testing.T) {
			s, plan, entries := geoipStoreFixture(t, existing)
			ctx := context.Background()
			tx, err := s.Prepare(ctx, plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Validate(ctx); err != nil {
				t.Fatal(err)
			}
			if !existing {
				if _, err := os.Stat(filepath.Join(s.assetDir, s.userGeoip)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("geoip published before commit", err)
				}
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(s.assetDir, s.userGeoip))
			if err != nil || !bytes.Equal(data, emptyGeoipFixture) {
				t.Fatal("invalid geoip", err)
			}
			config, err := os.ReadFile(entries[0].SystemPath)
			if err != nil || !bytes.Contains(config, []byte("ext:geoip_custom.dat:USERLIST")) {
				t.Fatal("geoip binding missing", err)
			}
			// Simulate daemon restart: recovery uses only durable manifest/candidate evidence.
			fresh, err := newFileStore(s.root, s.entries, s.validate)
			if err != nil {
				t.Fatal(err)
			}
			fresh.assetDir, fresh.userGeoip, fresh.userGeosite = s.assetDir, s.userGeoip, s.userGeosite
			if err := fresh.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(s.geositePath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("new geosite retained", err)
			}
			data, err = os.ReadFile(filepath.Join(s.assetDir, s.userGeoip))
			if existing {
				info, e := os.Stat(filepath.Join(s.assetDir, s.userGeoip))
				if err != nil || e != nil || !bytes.Equal(data, emptyGeoipFixture) || info.Mode().Perm() != 0640 {
					t.Fatal("existing geoip changed", err, e)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal("new geoip retained", err)
			}
		})
	}
}

func TestSetupGeoipPublicationFailureRecoversFirstAsset(t *testing.T) {
	s, plan, entries := geoipStoreFixture(t, false)
	ctx := context.Background()
	tx, err := s.Prepare(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	s.fault = func(stage, path string) error {
		if stage == "geoip-publish" {
			return errors.New("fixture geoip publish failure")
		}
		return nil
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("publish failure ignored")
	}
	if _, err := os.Stat(s.geositePath()); err != nil {
		t.Fatal("fixture did not reach second publication", err)
	}
	s.fault = nil
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{s.geositePath(), filepath.Join(s.assetDir, s.userGeoip)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("partial asset survived rollback", err)
		}
	}
	raw, err := os.ReadFile(entries[0].SystemPath)
	if err != nil || len(raw) != 0 {
		t.Fatal("system config changed", err)
	}
}

func TestSetupGeoipDriftBlocksWholeRecovery(t *testing.T) {
	s, plan, entries := geoipStoreFixture(t, false)
	ctx := context.Background()
	tx, err := s.Prepare(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.assetDir, s.userGeoip), []byte("external change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(ctx); err == nil {
		t.Fatal("geoip drift accepted")
	}
	for _, path := range []string{s.pendingPath(), s.geositePath(), entries[0].SystemPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("partial recovery or evidence lost", err)
		}
	}
	raw, err := os.ReadFile(entries[0].SystemPath)
	if err != nil || len(raw) == 0 {
		t.Fatal("config restored before pair verification", err)
	}
}

func TestSetupGeoipSettingsChangeBlocksRecovery(t *testing.T) {
	s, plan, _ := geoipStoreFixture(t, false)
	ctx := context.Background()
	tx, err := s.Prepare(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	original := s.userGeoip
	s.userGeoip = "geoip_different.dat"
	if err := s.Recover(ctx); err == nil {
		t.Fatal("changed geoip setting accepted")
	}
	s.userGeoip = original
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSetupGeoipChangedCandidatePublishesNoAssets(t *testing.T) {
	s, plan, _ := geoipStoreFixture(t, false)
	ctx := context.Background()
	tx, err := s.Prepare(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(s.root, tx.(*fileTransaction).manifest.ID, "user-geoip", s.userGeoip)
	if err := os.WriteFile(candidate, []byte("changed candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("changed candidate accepted")
	}
	for _, path := range []string{s.geositePath(), filepath.Join(s.assetDir, s.userGeoip)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("asset published before pair verified", err)
		}
	}
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSetupGeoipRejectsMalformedPairEvidence(t *testing.T) {
	for _, kind := range []string{"missing", "name", "directory", "hash", "version"} {
		t.Run(kind, func(t *testing.T) {
			s, plan, _ := geoipStoreFixture(t, false)
			ctx := context.Background()
			tx, err := s.Prepare(ctx, plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Validate(ctx); err != nil {
				t.Fatal(err)
			}
			manifest := tx.(*fileTransaction).manifest
			switch kind {
			case "missing":
				manifest.UserGeoip = nil
			case "name":
				manifest.UserGeoip.Name = "../geoip_custom.dat"
			case "directory":
				manifest.UserGeoip.Directory = filepath.Dir(s.assetDir)
			case "hash":
				manifest.UserGeoip.Hash = "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"
			case "version":
				manifest.Version = 4
			}
			if err := s.validateManifest(manifest); err == nil {
				t.Fatal("malformed pair evidence accepted")
			}
			// Restore from the unmodified durable manifest, not the altered in-memory copy.
			if err := s.Recover(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSetupGeoipPairAndNetworkRecoverTogether(t *testing.T) {
	s, _, entries := geoipStoreFixture(t, false)
	ctx := context.Background()
	path := filepath.Join(filepath.Dir(s.root), "helper.toml")
	original := []byte("# original helper config\n[geodata]\nuser_geoip = 'geoip_custom.dat'\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureNetwork(s, path); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AnalyzeGateway(ctx, Plan{Interface: "br-test", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.Prepare(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	fresh, err := newFileStore(s.root, s.entries, s.validate)
	if err != nil {
		t.Fatal(err)
	}
	fresh.assetDir = s.assetDir
	if err := ConfigureNetwork(fresh, path); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, original) {
		t.Fatal("helper settings not restored with assets", err)
	}
	for _, path := range []string{s.geositePath(), filepath.Join(s.assetDir, s.userGeoip)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("new asset survived network rollback", err)
		}
	}
	raw, err = os.ReadFile(entries[0].SystemPath)
	if err != nil || len(raw) != 0 {
		t.Fatal("Xray config not restored", err)
	}
}
