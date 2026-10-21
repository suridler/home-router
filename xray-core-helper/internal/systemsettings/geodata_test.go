package systemsettings

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/testconfig"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestGeodataDefaultsManualEditsAndOwnedSections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.toml")
	raw := "# administrator comment\n[logging]\nlevel = 'warning' # preserve this comment\n[geodata]\nmax_asset_size_mb = 512\norigins = []\n[scheduler]\ngeodata_update_enabled = false\n"
	if err := os.WriteFile(path, []byte(raw), 0640); err != nil {
		t.Fatal(err)
	}
	cfg, err := testconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, generation, err := service.Geodata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Geodata.MaxAssetSizeMB != 512 || len(got.Geodata.Origins) != 0 || got.Scheduler.GeodataUpdateEnabled || got.Scheduler.GeodataUpdateTime != "04:00" || got.Geodata.Geosite.URL == "" {
		t.Fatal("defaults replaced explicit values or failed to fill missing values", got.Geodata)
	}
	got.Geodata.MaxAssetSizeMB = 1024
	if _, _, err := service.PutGeodata(ctx, generation, got.Geodata, got.Scheduler); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "level = 'warning' # preserve this comment") {
		t.Fatal("unrelated TOML rewritten")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatal("permissions changed")
	}
	if _, _, err := service.PutGeodata(ctx, generation, got.Geodata, got.Scheduler); !errors.Is(err, ErrSettingsDrift) {
		t.Fatal("stale generation accepted", err)
	}
	// Read after manual editing, without rebuilding or restarting the service.
	manual := strings.Replace(string(encoded), "max_asset_size_mb = 1024", "max_asset_size_mb = 2048", 1)
	if err := os.WriteFile(path, []byte(manual), 0640); err != nil {
		t.Fatal(err)
	}
	next, _, err := service.Geodata(ctx)
	if err != nil || next.Geodata.MaxAssetSizeMB != 2048 {
		t.Fatal("manual edit not observed", err)
	}
}

func TestConcurrentGeodataSavesDoNotOverwriteEachOther(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("# defaults\n"), 0600); err != nil {
		t.Fatal(err)
	}
	service, err := New(path, testconfig.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	cfg, generation, err := service.Geodata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Geodata.MaxAssetSizeMB = 512
	results := make(chan error, 2)
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _, err := service.PutGeodata(ctx, generation, cfg.Geodata, cfg.Scheduler)
			results <- err
		}()
	}
	group.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrSettingsDrift) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
}

func TestGeodataEditPreservesValidTOMLForms(t *testing.T) {
	for _, raw := range []string{
		"geodata = { max_asset_size_mb = 512 }\n# keep\n[logging]\nlevel = 'warning'\n",
		"['geodata']\nmax_asset_size_mb = 512\n# keep\n[logging]\nlevel='warning'\n",
		"[geodata]\nmax_asset_size_mb = 512\norigins = [\n'https://raw.githubusercontent.com'\n]\n# keep\n[logging]\nlevel = '''warning'''\n",
	} {
		t.Run(raw[:8], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := testconfig.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			s, err := New(path, cfg)
			if err != nil {
				t.Fatal(err)
			}
			current, generation, err := s.Geodata(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			current.Geodata.MaxAssetSizeMB = 1024
			if _, _, err := s.PutGeodata(context.Background(), generation, current.Geodata, current.Scheduler); err != nil {
				t.Fatal(err)
			}
			next, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(next), "# keep") {
				t.Fatal("comment lost")
			}
			loaded, err := testconfig.Load(path)
			if err != nil || loaded.Geodata.MaxAssetSizeMB != 1024 {
				t.Fatal("invalid edit", err)
			}
		})
	}
}
