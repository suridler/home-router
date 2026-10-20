package loggingservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/logpathcheck"
	"home-router/xray-core-helper/internal/nativerender"
	"home-router/xray-core-helper/internal/systemsettings"
	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/xrayconfig"
)

func TestSettingsAndXrayShareSuccessfulAndFailedLoggingMutations(t *testing.T) {
	for _, failure := range []string{"", "validation", "restart", "files", "confirmed-restart", "confirmed-helper-restart", "confirmed-canceled-restart", "confirmed-validation", "confirmed-without-restriction"} {
		t.Run("failure-"+failure, func(t *testing.T) {
			resetMutationGate(t)
			root := t.TempDir()
			configPath := filepath.Join(root, "xray.json")
			helperPath := filepath.Join(root, "helper.toml")
			if err := os.WriteFile(configPath, []byte(loggingFixture), 0640); err != nil {
				t.Fatal(err)
			}
			originalTOML := []byte("# keep on failure\n[logging]\naccess_path='/custom/access.log'\nerror_path='/custom/errors.log'\nlevel='warning'\ndns_log=false\n")
			if err := os.WriteFile(helperPath, originalTOML, 0640); err != nil {
				t.Fatal(err)
			}
			cfg, err := bootstrap.Load(helperPath)
			if err != nil {
				t.Fatal(err)
			}
			settings, err := systemsettings.New(helperPath, cfg)
			if err != nil {
				t.Fatal(err)
			}
			store, err := transaction.OpenStore(context.Background(), filepath.Join(root, "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			events := &eventLog{}
			validator := &fakeValidator{events: events}
			restarter := &fakeRestarter{events: events}
			preparer := &fakePreparer{events: events, snap: &fakeSnapshot{events: events}}
			switch failure {
			case "validation", "confirmed-validation":
				validator.err = errors.New("reject")
			case "restart", "confirmed-restart", "confirmed-helper-restart", "confirmed-canceled-restart", "confirmed-without-restriction":
				restarter.err = errors.New("restart rejected")
			case "files":
				preparer.err = errors.New("file rejected")
			}
			engine, err := transaction.NewEngine(configPath, store, validator, restarter)
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Initialize(context.Background()); err != nil {
				t.Fatal(err)
			}
			settings.SetLoggingMetadata(store)
			service, err := New(engine, preparer, noOpRotationPreparer{})
			if err != nil {
				t.Fatal(err)
			}
			var preparedValues bootstrap.LoggingConfig
			if err := service.SetSettings(settings, func(values bootstrap.LoggingConfig) (FilePreparer, error) {
				preparedValues = values
				return preparer, nil
			}); err != nil {
				t.Fatal(err)
			}
			request := validRequest(0, true)
			confirmed := strings.HasPrefix(failure, "confirmed-")
			if confirmed {
				restricted := failure != "confirmed-without-restriction"
				if err := service.SetPathPolicy(fakePathChecker{restricted: restricted, helper: failure == "confirmed-helper-restart"}); err != nil {
					t.Fatal(err)
				}
				// The initial request must only warn: no file preparation,
				// restart, TOML mutation or new generation.
				_, warning := service.Update(context.Background(), request)
				code, _, _ := transaction.ErrorInfo(warning)
				if restricted {
					if code != "log_path_confirmation_required" || preparer.calls != 0 || restarter.calls != 0 {
						t.Fatalf("warning changed state: %v", warning)
					}
					got, _ := os.ReadFile(helperPath)
					if string(got) != string(originalTOML) {
						t.Fatal("warning rewrote TOML")
					}
				} else {
					// No warning is expected; test the confirmed request itself below.
					restarter.calls = 0
				}
				request.ConfirmRestrictedPaths = true
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "confirmed-canceled-restart" {
				restarter.cancel = cancel
			}
			_, err = service.Update(ctx, request)
			if (err != nil) != (failure != "") {
				t.Fatalf("mutation = %v", err)
			}
			actual, _ := os.ReadFile(configPath)
			if failure == "confirmed-restart" || failure == "confirmed-helper-restart" || failure == "confirmed-canceled-restart" {
				code, _, _ := transaction.ErrorInfo(err)
				if code != "logging_saved_restart_failed" || restarter.calls != 1 {
					t.Fatalf("restart failure was hidden or rolled back: %v calls=%d", err, restarter.calls)
				}
				persisted, loadErr := bootstrap.Load(helperPath)
				if loadErr != nil || persisted.Logging != preparedValues || !strings.Contains(string(actual), "/custom/access.log") {
					t.Fatal("confirmed settings not retained")
				}
				metadata, _ := store.Metadata(context.Background())
				pending, _ := store.Pending(context.Background())
				if metadata.Generation != 1 || pending != nil {
					t.Fatal("retained configuration not committed")
				}
				if failure == "confirmed-helper-restart" && preparer.last.Access.Mode != api.LogTargetJournal {
					t.Fatal("attempted to prepare a helper-restricted path")
				}
			} else if failure != "" {
				restored, _ := os.ReadFile(helperPath)
				if string(actual) != loggingFixture || string(restored) != string(originalTOML) {
					t.Fatal("failed operation did not restore both configurations")
				}
				if settings.Logging() != cfg.Logging {
					t.Fatal("failed mutation changed settings cache")
				}
			} else {
				if preparedValues.AccessPath != "/custom/access.log" || preparedValues.ErrorPath != "/custom/errors.log" {
					t.Fatalf("configured paths replaced: %+v", preparedValues)
				}
				for _, want := range []string{"/custom/access.log", "/custom/errors.log", "// keep logging comment", `"unknownLogOption": true`} {
					if !strings.Contains(string(actual), want) {
						t.Fatalf("Xray lost %q", want)
					}
				}
				persisted, err := bootstrap.Load(helperPath)
				if err != nil || persisted.Logging != preparedValues {
					t.Fatalf("TOML and Xray differ: %+v, %v", persisted.Logging, err)
				}
				// Render through the same dynamic provider used by daemon template apply.
				values := xrayconfig.TemplateValues{Logging: cfg.Logging}
				renderer := nativerender.Renderer{Xray: &values, Logging: settings.Logging}
				raw := []byte("{\n// helper:v1 bind level type=log-level source=helper.logging.level\n\"loglevel\": ${level}\n}\n")
				rendered, err := renderer.RenderXray(context.Background(), raw)
				if err != nil || !strings.Contains(string(rendered), `"info"`) {
					t.Fatalf("later apply reverted API settings: %s, %v", rendered, err)
				}
				request.ExpectedGeneration = new(uint64)
				*request.ExpectedGeneration = 1
				again, err := service.Update(context.Background(), request)
				if err != nil || again.Generation != 1 {
					t.Fatalf("idempotent mutation=%+v, %v", again, err)
				}
				if _, err := settings.Put(context.Background(), api.UpdateSystemSettingsRequest{Backup: api.BackupSettings{RetainPerTemplate: 21}}); err != nil {
					t.Fatal(err)
				}
				persisted, _ = bootstrap.Load(helperPath)
				if persisted.Logging != preparedValues {
					t.Fatal("settings PUT reverted logging")
				}
			}
			if err := settings.RecoverLogging(context.Background()); err != nil {
				t.Fatalf("operation left pending recovery: %v", err)
			}
		})
	}
}

type fakePathChecker struct{ restricted, helper bool }

func (f fakePathChecker) Check(context.Context, bootstrap.LoggingConfig) (logpathcheck.Result, error) {
	return logpathcheck.Result{XrayAccess: f.restricted && !f.helper, HelperAccess: f.restricted && f.helper}, nil
}

func TestResolvedLoggingModesPreserveConfiguredPathsAndDefaults(t *testing.T) {
	current := bootstrap.LoggingConfig{AccessPath: "/custom/a.log", ErrorPath: "/custom/e.log", Level: "warning"}
	request := validRequest(0, false).LogConfigurationRequest
	got, err := xrayconfig.ResolveLogConfiguration(request, current)
	if err != nil || got.AccessPath != current.AccessPath || got.ErrorPath != current.ErrorPath {
		t.Fatal(got, err)
	}
	request.Access.Mode = api.LogTargetDisabled
	request.Error.Mode = api.LogTargetJournal
	got, err = xrayconfig.ResolveLogConfiguration(request, current)
	if err != nil || got.AccessPath != "none" || got.ErrorPath != "" {
		t.Fatal(got, err)
	}
	request.Access.Mode = api.LogTargetFile
	request.Error.Mode = api.LogTargetFile
	got, err = xrayconfig.ResolveLogConfiguration(request, got)
	if err != nil || got.AccessPath != "/var/log/xray/access.log" || got.ErrorPath != "/var/log/xray/error.log" {
		t.Fatal(got, err)
	}
}
