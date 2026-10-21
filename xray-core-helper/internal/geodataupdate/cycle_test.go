package geodataupdate

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/testconfig"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/geodatadownload"
)

func TestCycleIndependentProcessesAndSingleRestart(t *testing.T) {
	for _, scenario := range []string{"success", "unchanged", "external-geosite", "external-geoip", "custom-geosite", "custom-geoip", "external-test", "custom-test", "write", "mixed-test", "restart", "routing", "all-failed", "cleanup-warning"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			cfg := testconfig.Defaults().Geodata
			store, err := domainstore.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddDesired(ctx, "example.com"); err != nil {
				t.Fatal(err)
			}
			old := []byte("old")
			next := []byte("new")
			for _, name := range []string{"geosite.dat", "geoip.dat", cfg.UserGeosite, cfg.UserGeoip} {
				if err := os.WriteFile(filepath.Join(dir, name), old, 0644); err != nil {
					t.Fatal(err)
				}
			}
			restarts, probes, fetches, builds := 0, 0, 0, 0
			token := "before"
			cycle, err := NewCycle(CycleOptions{RoutingAssets: activeUserAssets, AssetDir: dir, Store: store,
				Fetch: func(_ context.Context, _ bootstrap.GeodataConfig, name string) ([]byte, error) {
					fetches++
					if scenario == "all-failed" || scenario == "external-geosite" && name == "geosite.dat" || scenario == "external-geoip" && name == "geoip.dat" {
						return nil, geodatadownload.ErrChecksumMismatch
					}
					if scenario == "unchanged" {
						return old, nil
					}
					return next, nil
				},
				Build: func(_ context.Context, _ bootstrap.GeodataConfig, name string, _ []string) ([]byte, error) {
					builds++
					if scenario == "all-failed" || scenario == "custom-geosite" && name == cfg.UserGeosite || scenario == "custom-geoip" && name == cfg.UserGeoip {
						return nil, errors.New("builder")
					}
					if scenario == "unchanged" {
						return old, nil
					}
					if scenario == "cleanup-warning" {
						return next, &cleanupWarning{}
					}
					return next, nil
				},
				Validate: func(_ context.Context, _ bootstrap.GeodataConfig, paths map[string]string) error {
					probes++
					if scenario == "external-test" && paths["geosite.dat"] != "" || scenario == "custom-test" && paths[cfg.UserGeosite] != "" || scenario == "mixed-test" && paths == nil {
						return errors.New("test")
					}
					return nil
				},
				Replace: func(path string, data []byte) error {
					if (scenario == "write" || scenario == "mixed-test") && filepath.Base(path) == "geosite.dat" {
						return os.ErrPermission
					}
					return ReplaceAsset(path, data)
				},
				Restart: func(context.Context) error {
					restarts++
					if scenario == "restart" {
						return errors.New("start")
					}
					token = "after"
					return nil
				},
				Overlay: func(context.Context) error {
					if scenario == "routing" {
						return errors.New("routing")
					}
					return nil
				},
				Identity: func(context.Context) (string, error) { return token, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			report := cycle.UpdateReport(ctx, cfg)
			if fetches != 2 || builds != 2 {
				t.Fatalf("processes not attempted independently: fetch=%d build=%d", fetches, builds)
			}
			want := 1
			if scenario == "unchanged" || scenario == "all-failed" {
				want = 0
			}
			if restarts != want {
				t.Fatalf("restarts=%d report=%+v", restarts, report)
			}
			if (scenario == "success" || scenario == "cleanup-warning") && report.Replaced != 4 {
				t.Fatalf("report=%+v", report)
			}
			if scenario == "restart" || scenario == "routing" {
				if report.Activation == nil {
					t.Fatal("missing gateway failure")
				}
				data, _ := os.ReadFile(filepath.Join(dir, "geoip.dat"))
				if string(data) != "new" {
					t.Fatal("restart error rolled back files")
				}
			}
			if scenario == "write" {
				if probes != 3 {
					t.Fatalf("mixed set not tested: probes=%d", probes)
				}
			}
			if scenario == "mixed-test" {
				data, _ := os.ReadFile(filepath.Join(dir, "geoip.dat"))
				if string(data) != "old" {
					t.Fatal("failed mixed set not restored")
				}
				data, _ = os.ReadFile(filepath.Join(dir, cfg.UserGeosite))
				if string(data) != "new" {
					t.Fatal("custom success rolled back")
				}
			}
			snapshot, err := store.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Desired) != 1 {
				t.Fatal("desired resources lost")
			}
			if scenario == "success" && len(snapshot.Baseline) != 1 {
				t.Fatal("baseline not finalized")
			}
			if scenario == "restart" && len(snapshot.Baseline) != 0 {
				t.Fatal("baseline activated before Xray restart")
			}
		})
	}
}

func TestFailedRestoreKeepsEvidenceAndDoesNotRestartInvalidSet(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := testconfig.Defaults().Geodata
	store, err := domainstore.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, name := range []string{"geosite.dat", "geoip.dat"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	restarts := 0
	cycle, err := NewCycle(CycleOptions{RoutingAssets: activeUserAssets, AssetDir: dir, Store: store,
		Fetch: func(context.Context, bootstrap.GeodataConfig, string) ([]byte, error) { return []byte("new"), nil },
		Build: func(context.Context, bootstrap.GeodataConfig, string, []string) ([]byte, error) {
			t.Fatal("continued after critical restore failure")
			return nil, nil
		},
		Validate: func(_ context.Context, _ bootstrap.GeodataConfig, paths map[string]string) error {
			if paths == nil {
				return errors.New("mixed set rejected")
			}
			return nil
		},
		Replace: func(path string, data []byte) error {
			if filepath.Base(path) == "geosite.dat" || string(data) == "old" {
				return os.ErrPermission
			}
			return ReplaceAsset(path, data)
		},
		Identity: func(context.Context) (string, error) { return "runtime", nil },
		Restart:  func(context.Context) error { restarts++; return nil }, Overlay: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	report := cycle.UpdateReport(ctx, cfg)
	if restarts != 0 || report.Activation == nil || report.Activation.Stage != "restore" {
		t.Fatal("critical state hidden", report)
	}
	if report.EvidenceDirectory == "" {
		t.Fatal("no recovery evidence")
	}
	data, err := os.ReadFile(filepath.Join(report.EvidenceDirectory, "geoip.dat.previous"))
	if err != nil || string(data) != "old" {
		t.Fatal("previous version lost", err)
	}
}

func TestRetryActivatesFilesLeftByInterruptedOrFailedRestart(t *testing.T) {
	ctx := context.Background()
	assets := t.TempDir()
	db := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := domainstore.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testconfig.Defaults().Geodata
	if err := store.AddDesired(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	token := "old"
	restarts := 0
	options := CycleOptions{RoutingAssets: activeUserAssets, AssetDir: assets, Store: store,
		Fetch: func(context.Context, bootstrap.GeodataConfig, string) ([]byte, error) {
			return []byte("verified external"), nil
		},
		Build: func(context.Context, bootstrap.GeodataConfig, string, []string) ([]byte, error) {
			return []byte("verified custom"), nil
		},
		Validate: func(context.Context, bootstrap.GeodataConfig, map[string]string) error { return nil },
		Identity: func(context.Context) (string, error) { return token, nil },
		Overlay:  func(context.Context) error { return nil },
		Restart: func(context.Context) error {
			restarts++
			if restarts == 1 {
				return errors.New("restart interrupted")
			}
			token = "new"
			return nil
		},
	}
	cycle, err := NewCycle(options)
	if err != nil {
		t.Fatal(err)
	}
	report := cycle.UpdateReport(ctx, cfg)
	if report.Replaced != 4 || report.Activation == nil {
		t.Fatal(report)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = domainstore.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	options.Store = store
	cycle, err = NewCycle(options)
	if err != nil {
		t.Fatal(err)
	}
	report = cycle.UpdateReport(ctx, cfg)
	if report.Replaced != 0 || !report.Restarted || report.Activation != nil || restarts != 2 {
		t.Fatalf("unchanged disk files were not activated after retry: %+v restarts=%d", report, restarts)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || len(snapshot.Baseline) != 1 {
		t.Fatal(snapshot, err)
	}
	report = cycle.UpdateReport(ctx, cfg)
	if report.Restarted || restarts != 2 {
		t.Fatal("duplicate restart", report)
	}
}

func activeUserAssets(context.Context) (map[string]bool, error) {
	return map[string]bool{"geosite_user.dat": true, "geoip_user.dat": true}, nil
}

func TestUnboundUserlistKeepsDynamicRulesUntilBinding(t *testing.T) {
	ctx := context.Background()
	assets := t.TempDir()
	store, err := domainstore.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddDesired(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	cfg := testconfig.Defaults().Geodata
	cfg.UserGeosite = "geosite_new.dat"
	bound := false
	token := "old"
	restarts := 0
	cycle, err := NewCycle(CycleOptions{AssetDir: assets, Store: store,
		RoutingAssets: func(context.Context) (map[string]bool, error) {
			return map[string]bool{cfg.UserGeosite: bound, cfg.UserGeoip: true}, nil
		},
		Fetch: func(context.Context, bootstrap.GeodataConfig, string) ([]byte, error) { return []byte("external"), nil },
		Build: func(context.Context, bootstrap.GeodataConfig, string, []string) ([]byte, error) {
			return []byte("custom"), nil
		},
		Validate: func(context.Context, bootstrap.GeodataConfig, map[string]string) error { return nil },
		Identity: func(context.Context) (string, error) { return token, nil },
		Restart:  func(context.Context) error { restarts++; token = "new"; return nil },
		Overlay: func(ctx context.Context) error {
			snapshot, err := store.Snapshot(ctx)
			if err != nil {
				return err
			}
			if len(snapshot.Baseline) != 0 || len(snapshot.Desired) != 1 {
				t.Fatal("unbound file removed runtime additions", snapshot)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	report := cycle.UpdateReport(ctx, cfg)
	if report.Activation != nil || report.Replaced != 4 || !report.Restarted {
		t.Fatal(report)
	}
	if _, err := os.Stat(filepath.Join(assets, cfg.UserGeosite)); err != nil {
		t.Fatal(err)
	}
	report = cycle.UpdateReport(ctx, cfg)
	if report.Restarted || restarts != 1 {
		t.Fatal("unbound file caused repeated restart", report)
	}
	bound = true
	active, _ := cycle.options.RoutingAssets(ctx)
	if err := store.ActivateBaselines(ctx, "after-binding", assets, active); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || len(snapshot.Baseline) != 1 || snapshot.Baseline[0] != "example.com" {
		t.Fatal(snapshot, err)
	}
}
