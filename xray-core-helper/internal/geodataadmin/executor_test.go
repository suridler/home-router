package geodataadmin

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/geodatadownload"
	"home-router/xray-core-helper/internal/geodatalimits"
	"home-router/xray-core-helper/internal/geodatarecovery"
	"home-router/xray-core-helper/internal/geodataupdate"
)

type executorRecovery struct{ status geodatarecovery.Status }

func (recovery executorRecovery) Status() geodatarecovery.Status { return recovery.status }

type updateFake struct {
	calls  int
	config bootstrap.GeodataConfig
	err    error
}

func (updater *updateFake) Update(_ context.Context, config bootstrap.GeodataConfig) error {
	updater.calls++
	updater.config = config
	return updater.err
}

func executorFixture(t *testing.T, recovery geodatarecovery.Status, updater Updater) (*Service, *Executor) {
	t.Helper()
	resetCoordinatorForTest(t)
	t.Cleanup(func() { resetCoordinatorForTest(t) })
	initial := bootstrap.GeodataConfig{
		Origins: []string{"https://initial.example"},
		Builder: "/trusted/static/domain-list-builder",
		Geosite: bootstrap.GeodataSourceConfig{
			URL: "https://initial.example/geosite.dat",
			Checksum: bootstrap.GeodataChecksumConfig{
				URL: "https://initial.example/geosite.sha256", Algorithm: "sha256",
			},
		},
		Geoip: bootstrap.GeodataSourceConfig{
			URL: "https://initial.example/geoip.dat",
			Checksum: bootstrap.GeodataChecksumConfig{
				URL: "https://initial.example/geoip.sha256", Algorithm: "sha256",
			},
		},
	}
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"), initial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := NewService(ServiceOptions{Store: store, Recovery: executorRecovery{status: recovery}})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(service, initial, updater)
	if err != nil {
		t.Fatal(err)
	}
	return service, executor
}

func executorCleanRecovery() geodatarecovery.Status {
	return geodatarecovery.Status{
		Code: geodatarecovery.RecoveryNone.String(),
		Result: geodatarecovery.Result{
			State: geodatarecovery.State{Phase: geodatarecovery.PhaseClean},
			Plan:  geodatarecovery.Plan{Phase: geodatarecovery.PhaseClean},
		},
	}
}

func TestExecutorUsesPersistedSettingsAndReplaysWithoutRunningAgain(t *testing.T) {
	updater := &updateFake{}
	service, executor := executorFixture(t, executorCleanRecovery(), updater)
	persisted := Settings{
		Origins: []string{"https://persisted.example"},
		Geosite: SourceSettings{URL: "https://persisted.example/site.dat",
			Checksum: ChecksumSettings{URL: "https://persisted.example/site.sha256", Algorithm: "sha256"}},
		Geoip: SourceSettings{URL: "https://persisted.example/ip.dat",
			Checksum: ChecksumSettings{URL: "https://persisted.example/ip.sha256", Algorithm: "sha256"}},
	}
	if _, err := service.ReplaceSettings(context.Background(), 0, persisted); err != nil {
		t.Fatal(err)
	}
	first, err := executor.Execute(context.Background(), "update-1")
	if err != nil || first.State != OperationSucceeded {
		t.Fatalf("Execute() = %#v, err=%v; want durable success", first, err)
	}
	if updater.calls != 1 || updater.config.Builder != "/trusted/static/domain-list-builder" ||
		updater.config.Geosite.URL != persisted.Geosite.URL ||
		updater.config.Geoip.Checksum.URL != persisted.Geoip.Checksum.URL ||
		updater.config.Origins[0] != persisted.Origins[0] {
		t.Fatalf("updater calls=%d config=%#v; persisted values or trusted builder were not used", updater.calls, updater.config)
	}
	replay, err := executor.Execute(context.Background(), "update-1")
	if err != nil || replay.ID != first.ID || replay.State != OperationSucceeded || updater.calls != 1 {
		t.Fatalf("replay = %#v, err=%v calls=%d; update must not execute twice", replay, err, updater.calls)
	}
}

func TestExecutorRejectsActiveRequestAndConflictingKey(t *testing.T) {
	updater := &updateFake{}
	service, executor := executorFixture(t, executorCleanRecovery(), updater)
	running, _, err := service.BeginUpdate(context.Background(), "running-key")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := executor.Execute(context.Background(), "running-key")
	if err != nil || replayed.ID != running.ID || updater.calls != 0 {
		t.Fatalf("same-key running replay = %#v, err=%v calls=%d", replayed, err, updater.calls)
	}
	if _, err := executor.Execute(context.Background(), "other-key"); !executionErrorIs(err, "geodata_update_in_progress", 409) {
		t.Fatalf("active-operation error = %v", err)
	}
}

func TestExecutorRejectsAKeyReusedWithDifferentPersistedSettings(t *testing.T) {
	updater := &updateFake{}
	service, executor := executorFixture(t, executorCleanRecovery(), updater)
	if _, _, err := service.BeginUpdate(context.Background(), "reused-key"); err != nil {
		t.Fatal(err)
	}
	changed := Settings{
		Origins: []string{"https://changed.example"},
		Geosite: SourceSettings{URL: "https://changed.example/site.dat",
			Checksum: ChecksumSettings{URL: "https://changed.example/site.sha256", Algorithm: "sha256"}},
		Geoip: SourceSettings{URL: "https://changed.example/ip.dat",
			Checksum: ChecksumSettings{URL: "https://changed.example/ip.sha256", Algorithm: "sha256"}},
	}
	if _, err := service.ReplaceSettings(context.Background(), 0, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), "reused-key"); !executionErrorIs(err, "idempotency_conflict", 409) {
		t.Fatalf("reused key error = %v", err)
	}
	if updater.calls != 0 {
		t.Fatalf("updater calls=%d; conflicting key must not execute", updater.calls)
	}
}

func TestExecutorBlocksRecoveryAndUnavailableUpdater(t *testing.T) {
	blocked := geodatarecovery.Status{
		Code: geodatarecovery.ActionRetryInspection, MutationsBlocked: true,
		Result: geodatarecovery.Result{State: geodatarecovery.State{Phase: geodatarecovery.PhaseInspectionFailed}},
	}
	_, executor := executorFixture(t, blocked, &updateFake{})
	if _, err := executor.Execute(context.Background(), "blocked-key"); !executionErrorIs(err, "geodata_recovery_required", 503) {
		t.Fatalf("recovery error = %v", err)
	}

	_, unavailable := executorFixture(t, executorCleanRecovery(), nil)
	if _, err := unavailable.Execute(context.Background(), "unavailable-key"); !executionErrorIs(err, "geodata_update_unavailable", 503) {
		t.Fatalf("unavailable error = %v", err)
	}
}

func TestExecutorMapsAndPersistsUpdaterFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code FailureCode
	}{
		{name: "download", err: geodataupdate.ErrDownloadFailed, code: FailureDownloadFailed},
		{name: "asset too large", err: errors.Join(geodataupdate.ErrDownloadFailed, &geodatalimits.Exceeded{LimitBytes: 256 << 20}), code: FailureAssetTooLarge},
		{name: "checksum", err: errors.Join(geodataupdate.ErrDownloadFailed, geodatadownload.ErrChecksumMismatch), code: FailureChecksumFailed},
		{name: "validation", err: geodataupdate.ErrValidationFailed, code: FailureValidationFailed},
		{name: "install", err: geodataupdate.ErrInstallFailed, code: FailureInstallFailed},
		{name: "restart", err: geodataupdate.ErrRestartFailed, code: FailureRestartFailed},
		{name: "generic", err: errors.New("secret external output"), code: FailureUpdateFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updater := &updateFake{err: test.err}
			service, executor := executorFixture(t, executorCleanRecovery(), updater)
			if _, err := executor.Execute(context.Background(), "failed-key"); !executionErrorIs(err, string(test.code), 503) {
				t.Fatalf("Execute() error = %v", err)
			}
			status, err := service.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if status.Operation == nil || status.Operation.State != OperationFailed ||
				status.Operation.ErrorCode != string(test.code) {
				t.Fatalf("persisted operation = %#v", status.Operation)
			}
			if _, err := executor.Execute(context.Background(), "failed-key"); !executionErrorIs(err, string(test.code), 503) || updater.calls != 1 {
				t.Fatalf("failed replay err=%v updater calls=%d", err, updater.calls)
			}
		})
	}
}

func TestExecutorMarksRollbackFailureRecoveryRequired(t *testing.T) {
	updater := &updateFake{err: geodataupdate.ErrRollbackFailed}
	service, executor := executorFixture(t, executorCleanRecovery(), updater)
	if _, err := executor.Execute(context.Background(), "recovery-key"); !executionErrorIs(err, "geodata_recovery_required", 503) {
		t.Fatalf("Execute() error = %v", err)
	}
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Operation == nil || status.Operation.State != OperationRecoveryRequired {
		t.Fatalf("persisted operation = %#v; want recovery_required", status.Operation)
	}
	if !coordinator.RecoveryBlocked() {
		t.Fatal("rollback failure did not block the mutation coordinator")
	}
	if _, err := executor.Execute(context.Background(), "later-key"); !executionErrorIs(err, "geodata_recovery_required", 503) {
		t.Fatalf("later update error = %v; want recovery-required refusal", err)
	}
	if updater.calls != 1 {
		t.Fatalf("updater calls=%d; recovery-blocked update executed", updater.calls)
	}
	currentSettings, err := service.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	replacement := currentSettings.Settings
	if _, err := service.ReplaceSettings(context.Background(), currentSettings.Generation, replacement); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("settings replacement error = %v; want recovery-required", err)
	}
	unchanged, err := service.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Generation != currentSettings.Generation {
		t.Fatalf("settings generation changed from %d to %d while recovery blocked", currentSettings.Generation, unchanged.Generation)
	}
	replay, replayed, err := service.BeginUpdate(context.Background(), "recovery-key")
	if err != nil || !replayed || replay.ID != status.Operation.ID ||
		replay.State != OperationRecoveryRequired || updater.calls != 1 {
		t.Fatalf("same-key recovery replay=%#v replayed=%t err=%v calls=%d", replay, replayed, err, updater.calls)
	}
	if _, err := executor.Execute(context.Background(), "recovery-key"); !executionErrorIs(err, "geodata_recovery_required", 503) ||
		updater.calls != 1 {
		t.Fatalf("executor replay err=%v updater calls=%d", err, updater.calls)
	}
}

func TestCoordinatorBlockRacingAdmissionFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(context.Context, *Service) error
	}{
		{
			name: "new update",
			run: func(ctx context.Context, service *Service) error {
				_, _, _, err := service.BeginUpdateSnapshot(ctx, "race-update")
				return err
			},
		},
		{
			name: "settings replacement",
			run: func(ctx context.Context, service *Service) error {
				_, err := service.ReplaceSettings(ctx, 0, configuredSettings())
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _ := executorFixture(t, executorCleanRecovery(), &updateFake{})
			held, err := coordinator.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				close(started)
				result <- test.run(context.Background(), service)
			}()
			<-started
			coordinator.Block()
			held.Release()
			if err := <-result; !errors.Is(err, ErrRecoveryRequired) {
				t.Fatalf("racing mutation error=%v; want recovery-required", err)
			}
			settings, err := service.Settings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if settings.Generation != 0 {
				t.Fatalf("settings generation=%d; mutation crossed blocked admission", settings.Generation)
			}
			var operations int
			if err := service.store.db.QueryRow("SELECT count(*) FROM geodata_update_operation").Scan(&operations); err != nil {
				t.Fatal(err)
			}
			if operations != 0 {
				t.Fatalf("operation rows=%d; admission crossed blocked gate", operations)
			}
			if !coordinator.RecoveryBlocked() {
				t.Fatal("racing admission unblocked coordinator")
			}
		})
	}
}

type cancelUpdateFake struct{ cancel context.CancelFunc }

func (updater cancelUpdateFake) Update(context.Context, bootstrap.GeodataConfig) error {
	updater.cancel()
	return errors.Join(geodataupdate.ErrDownloadFailed, context.Canceled)
}

func TestExecutorFinishesDurablyAfterRequestCancellation(t *testing.T) {
	service, _ := executorFixture(t, executorCleanRecovery(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	updater := cancelUpdateFake{cancel: cancel}
	executor, err := NewExecutor(service, bootstrap.GeodataConfig{
		Builder: "/trusted/static/domain-list-builder",
		Geosite: bootstrap.GeodataSourceConfig{Checksum: bootstrap.GeodataChecksumConfig{Algorithm: "sha256"}},
		Geoip:   bootstrap.GeodataSourceConfig{Checksum: bootstrap.GeodataChecksumConfig{Algorithm: "sha256"}},
	}, updater)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(ctx, "cancelled-key"); !executionErrorIs(err, string(FailureDownloadFailed), 503) {
		t.Fatalf("Execute() error = %v", err)
	}
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Operation == nil || status.Operation.State != OperationFailed {
		t.Fatalf("operation after cancellation = %#v; want durable failure", status.Operation)
	}
}

func executionErrorIs(err error, code string, status int) bool {
	var executionError *ExecutionError
	return errors.As(err, &executionError) && executionError.Code == code && executionError.HTTPStatus == status
}

func resetCoordinatorForTest(t *testing.T) {
	t.Helper()
	if !coordinator.RecoveryBlocked() {
		return
	}
	lease, err := coordinator.AcquireMaintenance(context.Background())
	if err != nil {
		t.Fatalf("acquire recovery lease to reset test gate: %v", err)
	}
	if err := lease.Unblock(); err != nil {
		lease.Release()
		t.Fatalf("unblock coordinator test gate: %v", err)
	}
	lease.Release()
}

func TestSizeFailureAndReplayIncludeConfiguredLimit(t *testing.T) {
	updater := &updateFake{err: errors.Join(geodataupdate.ErrDownloadFailed, &geodatalimits.Exceeded{LimitBytes: 512 << 20})}
	_, executor := executorFixture(t, executorCleanRecovery(), updater)
	executor.base.MaxAssetSizeMB = 512
	for attempt := 0; attempt < 2; attempt++ {
		_, err := executor.Execute(context.Background(), "size-replay")
		var failure *ExecutionError
		if !errors.As(err, &failure) || failure.Code != string(FailureAssetTooLarge) || failure.MaxAssetSizeMB != 512 {
			t.Fatal(err)
		}
	}
	if updater.calls != 1 {
		t.Fatal("replayed a failed update", updater.calls)
	}
}
