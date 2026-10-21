package geodataadmin

import (
	"context"
	"errors"
	"github.com/pelletier/go-toml/v2"
	"home-router/xray-core-helper/internal/testconfig"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/coordinator"
)

type reportUpdater struct {
	calls  int
	cfg    bootstrap.GeodataConfig
	report api.GeodataReport
	cancel context.CancelFunc
	after  func()
}

func (u *reportUpdater) UpdateReportProgress(ctx context.Context, cfg bootstrap.GeodataConfig, checkpoint func(api.GeodataReport) error) api.GeodataReport {
	u.calls++
	u.cfg = cfg
	if err := checkpoint(api.GeodataReport{Process: "external", Stage: "download"}); err != nil {
		panic(err)
	}
	if u.cancel != nil {
		u.cancel()
	}
	if u.after != nil {
		u.after()
	}
	return u.report
}

func resetCoordinatorForTest(t *testing.T) {
	t.Helper()
	if !coordinator.RecoveryBlocked() {
		return
	}
	lease, err := coordinator.AcquireMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Unblock(); err != nil {
		lease.Release()
		t.Fatal(err)
	}
	lease.Release()
}

func TestExecutorUsesTOMLSnapshotAndReplaysWithoutRepeatingWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, service := openFixture(t, path, bootstrapSettings(configuredSettings()), cleanRecovery())
	defer store.Close()
	updater := &reportUpdater{}
	executor, err := NewExecutor(service, testconfig.Defaults().Geodata, updater)
	if err != nil {
		t.Fatal(err)
	}
	changed := configuredSettings()
	changed.MaxAssetSizeMB = 512

	changed.Geosite.URL = "https://data.example/new.dat"
	if _, err := service.ReplaceSettings(context.Background(), currentGeneration(t, service), changed); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := service.settings.Geodata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Geodata.Builder = "/custom/builder"
	encoded, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The administrator edits TOML. The running executor must use this value.
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "config.toml"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	first, err := executeForTest(executor, context.Background(), "request-1")
	if err != nil || first.State != OperationSucceeded {
		t.Fatal(first, err)
	}
	if updater.cfg.MaxAssetSizeMB != 512 || updater.cfg.Builder != "/custom/builder" || updater.cfg.Geosite.URL != changed.Geosite.URL {
		t.Fatal("not a TOML snapshot", updater.cfg)
	}
	replay, err := executeForTest(executor, context.Background(), "request-1")
	if err != nil || replay.ID != first.ID || updater.calls != 1 {
		t.Fatal("duplicate execution", err)
	}
	changed.MaxAssetSizeMB = 1024
	changed.Builder = "/custom/builder"
	if _, err := service.ReplaceSettings(context.Background(), currentGeneration(t, service), changed); err != nil {
		t.Fatal(err)
	}
	if _, err := executeForTest(executor, context.Background(), "request-1"); !executionErrorIs(err, "idempotency_conflict", 409) {
		t.Fatal(err)
	}
}

func TestExecutorPersistsPartialResultsActivationAndWarnings(t *testing.T) {
	for _, kind := range []string{"partial", "gateway", "warning", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			store, service := openFixture(t, filepath.Join(t.TempDir(), "state.sqlite"), bootstrapSettings(configuredSettings()), cleanRecovery())
			defer store.Close()
			report := api.GeodataReport{Replaced: 1, Files: []api.GeodataFileResult{{File: "geoip.dat", Process: "external", State: "replaced"}}}
			issue := api.GeodataIssue{Process: "external", File: "geosite.dat", Stage: "checksum", Code: "geodata_external_checksum_failed", Message: "Контрольная сумма SHA256 не совпадает"}
			if kind == "partial" {
				report.Issues = []api.GeodataIssue{issue}
			}
			if kind == "gateway" {
				issue.Process = "xray"
				issue.Stage = "restart"
				issue.Code = "geodata_xray_restart_failed"
				report.Activation = &issue
			}
			if kind == "warning" {
				issue.Stage = "cleanup"
				issue.Warning = true
				report.Issues = []api.GeodataIssue{issue}
			}
			updater := &reportUpdater{report: report}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancelled" {
				updater.cancel = cancel
			}
			executor, err := NewExecutor(service, testconfig.Defaults().Geodata, updater)
			if err != nil {
				t.Fatal(err)
			}
			_, runErr := executeForTest(executor, ctx, "update")
			if (runErr != nil) != (kind == "partial" || kind == "gateway" || kind == "cancelled") {
				t.Fatal("unexpected result", runErr)
			}
			status, err := service.Status(context.Background())
			if err != nil || status.Operation == nil || status.Operation.Report == nil {
				t.Fatal("result not persisted", err)
			}
			if kind == "gateway" && status.Operation.State != OperationSucceeded {
				t.Fatal("gateway error marked file update failed")
			}
			if kind == "cancelled" && status.Operation.ErrorCode != string(FailureInterrupted) {
				t.Fatal("interruption hidden", status.Operation)
			}
			if kind == "partial" && status.Operation.State != OperationFailed {
				t.Fatal("partial failure hidden")
			}
			if kind == "warning" && status.Operation.State != OperationSucceeded {
				t.Fatal("cleanup warning marked update failed")
			}
			if coordinator.RecoveryBlocked() {
				t.Fatal("geodata blocked all mutations")
			}
			if status.Operation.Report.Replaced != 1 {
				t.Fatal("partial success lost")
			}
		})
	}
}

func TestInterruptedOperationBecomesRetryableAndKeepsReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, service := openFixture(t, path, bootstrapSettings(configuredSettings()), cleanRecovery())
	op, _, err := service.BeginUpdate(context.Background(), "interrupted")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.saveReport(context.Background(), op.ID, &api.GeodataReport{Process: "custom", Stage: "write"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, service := openFixture(t, path, bootstrapSettings(configuredSettings()), cleanRecovery())
	defer reopened.Close()
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Operation.State != OperationFailed || status.Operation.ErrorCode != "geodata_interrupted" || status.Operation.Report.Stage != "write" {
		t.Fatal("interrupted execution not reported", status.Operation)
	}
	if _, _, err := service.BeginUpdate(context.Background(), "retry"); err != nil {
		t.Fatal("retry blocked", err)
	}
}

func executionErrorIs(err error, code string, status int) bool {
	var e *ExecutionError
	return errors.As(err, &e) && e.Code == code && e.HTTPStatus == status
}

func TestExecutorReturnsKnownResultsWhenFinalPersistenceFails(t *testing.T) {
	store, service := openFixture(t, filepath.Join(t.TempDir(), "state.sqlite"), bootstrapSettings(configuredSettings()), cleanRecovery())
	updater := &reportUpdater{report: api.GeodataReport{Replaced: 1, Files: []api.GeodataFileResult{{File: "geoip.dat", State: "replaced"}}}, after: func() { store.Close() }}
	executor, err := NewExecutor(service, testconfig.Defaults().Geodata, updater)
	if err != nil {
		t.Fatal(err)
	}
	op, err := executeForTest(executor, context.Background(), "broken-final-state")
	if !executionErrorIs(err, "geodata_cycle_save_state_failed", 503) || op.ID == "" || op.Report == nil || op.Report.Replaced != 1 {
		t.Fatalf("lost final results: %+v %v", op, err)
	}
}

// Exercise completion and persistence failures synchronously; async admission and
// lifecycle are covered by async_test.go and the real HTTP contract tests.
func executeForTest(executor *Executor, ctx context.Context, key string) (Operation, error) {
	operation, settings, replayed, err := executor.service.BeginUpdateSnapshot(ctx, key)
	if err != nil {
		return Operation{}, admissionError(err)
	}
	if replayed {
		return operation, operationError(operation)
	}
	return executor.executeAdmitted(ctx, operation, settings)
}
