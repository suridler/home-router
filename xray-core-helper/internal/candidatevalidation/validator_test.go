package candidatevalidation

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/testconfig"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/xrayconfig"
)

type runFunc func(context.Context, string, []string, []string) error

func (f runFunc) Run(ctx context.Context, binary string, args, env []string) error {
	return f(ctx, binary, args, env)
}

type cleanupFailure struct{ osFileSystem }

func (cleanupFailure) RemoveAll(string) error { return os.ErrPermission }

func TestCandidatesUseCurrentCompanionsAndNeverChangeLiveFiles(t *testing.T) {
	for _, scenario := range []string{"standard", "custom", "mixed-live", "test-fails", "missing-companion", "oversized", "cleanup-warning", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			live := filepath.Join(root, "assets")
			if err := os.Mkdir(live, 0700); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(root, "config.json")
			// Include a normal companion basename, rather than the former country-code restriction.
			if err := os.WriteFile(configPath, []byte(`{"routing":{"rules":[{"domain":["geosite:private","ext:custom-list.dat:USERLIST","ext:geosite_user.dat:USERLIST"],"outboundTag":"direct"}]}}`), 0600); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"geosite.dat", "geoip.dat", "custom-list.dat", "geosite_user.dat"} {
				if err := os.WriteFile(filepath.Join(live, name), []byte("live "+name), 0664); err != nil {
					t.Fatal(err)
				}
			}
			cfg := testconfig.Defaults()
			cfg.Xray.Config = configPath
			calls := 0
			validator, err := New(cfg, Options{TempRoot: root, Runner: runFunc(func(_ context.Context, binary string, args, env []string) error {
				calls++
				if binary != cfg.Xray.Binary || strings.Join(args, " ") != "run -test -config "+configPath {
					t.Fatal("unexpected command", binary, args)
				}
				dir := strings.TrimPrefix(env[0], "XRAY_LOCATION_ASSET=")
				if dir == live {
					t.Fatal("live assets tested in place")
				}
				for _, name := range []string{"geosite.dat", "geoip.dat", "custom-list.dat", "geosite_user.dat"} {
					if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
						t.Fatal("companion not copied", name, err)
					}
				}
				data, _ := os.ReadFile(filepath.Join(dir, "geosite.dat"))
				if scenario == "standard" && string(data) != "candidate" {
					t.Fatal("candidate not substituted")
				}
				if scenario == "test-fails" {
					return errors.New("private config output")
				}
				return nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			validator.assetDir = live
			validator.assetReader = xrayconfig.NewReader(xrayconfig.Settings{Config: configPath, AssetDir: live})
			candidates := map[string]string{}
			candidatePath := filepath.Join(root, "candidate.dat")
			if err := os.WriteFile(candidatePath, []byte("candidate"), 0600); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "standard", "test-fails", "cleanup-warning":
				candidates["geosite.dat"] = candidatePath
			case "custom":
				candidates["geosite_user.dat"] = candidatePath
			case "missing-companion":
				if err := os.Remove(filepath.Join(live, "custom-list.dat")); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				validator.maxAssetSize = 3
			}
			if scenario == "cleanup-warning" {
				validator.files = cleanupFailure{}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancelled" {
				cancel()
			}
			err = validator.ValidateCandidates(ctx, candidates)
			wantError := scenario == "test-fails" || scenario == "missing-companion" || scenario == "oversized" || scenario == "cleanup-warning" || scenario == "cancelled"
			if (err != nil) != wantError {
				t.Fatal(scenario, err)
			}
			if scenario == "cleanup-warning" {
				var warning *CleanupWarning
				if !errors.As(err, &warning) {
					t.Fatal("cleanup invalidated test", err)
				}
			}
			if scenario == "test-fails" && strings.Contains(err.Error(), "private config") {
				t.Fatal("command output leaked")
			}
			if (scenario == "missing-companion" || scenario == "oversized" || scenario == "cancelled") && calls != 0 {
				t.Fatal("Xray called after preparation failure")
			}
			data, _ := os.ReadFile(filepath.Join(live, "geosite.dat"))
			if string(data) != "live geosite.dat" {
				t.Fatal("live file modified")
			}
		})
	}
}
