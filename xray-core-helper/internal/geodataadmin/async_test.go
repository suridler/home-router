package geodataadmin

import (
	"context"
	"home-router/xray-core-helper/internal/testconfig"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/coordinator"
)

type blockingUpdater struct {
	started chan context.Context
	release chan struct{}
}

func (u *blockingUpdater) UpdateReportProgress(ctx context.Context, _ bootstrap.GeodataConfig, checkpoint func(api.GeodataReport) error) api.GeodataReport {
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return api.GeodataReport{Issues: []api.GeodataIssue{{Code: "geodata_interrupted"}}}
	}
	defer lease.Release()
	if err := checkpoint(api.GeodataReport{Process: "external", Stage: "download"}); err != nil {
		panic(err)
	}
	u.started <- ctx
	select {
	case <-u.release:
		return api.GeodataReport{Replaced: 1}
	case <-ctx.Done():
		return api.GeodataReport{Process: "external", Stage: "download", Issues: []api.GeodataIssue{{Code: "geodata_interrupted", Stage: "download"}}}
	}
}

func TestAsyncAdmissionDisconnectReplayConflictAndCompletion(t *testing.T) {
	store, service := openFixture(t, filepath.Join(t.TempDir(), "state.sqlite"), bootstrapSettings(configuredSettings()), cleanRecovery())
	defer store.Close()
	u := &blockingUpdater{started: make(chan context.Context, 2), release: make(chan struct{})}
	executor, err := NewExecutor(service, testconfig.Defaults().Geodata, u)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { close(u.release) }) }
	defer executor.Close()
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	op, err := executor.Execute(ctx, "async-1")
	if err != nil || op.ID == "" || op.State != OperationRunning {
		t.Fatalf("admission: %+v %v", op, err)
	}
	var workerCtx context.Context
	select {
	case workerCtx = <-u.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker not started")
	}
	cancel()
	if workerCtx.Err() != nil {
		t.Fatal("request cancellation stopped accepted work")
	}
	replay, err := executor.Execute(context.Background(), "async-1")
	if err != nil || replay.ID != op.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if _, err := executor.Execute(context.Background(), "async-2"); !executionErrorIs(err, "geodata_update_in_progress", 409) {
		t.Fatalf("concurrent request: %v", err)
	}
	status, err := service.Status(context.Background())
	if err != nil || status.Operation.Report.Stage != "download" {
		t.Fatalf("checkpoint: %+v %v", status, err)
	}
	release()
	executor.workers.Wait()
	status, err = service.Status(context.Background())
	if err != nil || status.Operation.State != OperationSucceeded || status.Operation.Report.Replaced != 1 {
		t.Fatalf("completion: %+v %v", status, err)
	}
	replay, err = executor.Execute(context.Background(), "async-1")
	if err != nil || replay.State != OperationSucceeded {
		t.Fatalf("completed replay: %+v %v", replay, err)
	}
	select {
	case <-u.started:
		t.Fatal("replay ran updater again")
	default:
	}
}

func TestAsyncCloseCancelsAndJoinsWorkerBeforeStoreCloses(t *testing.T) {
	store, service := openFixture(t, filepath.Join(t.TempDir(), "state.sqlite"), bootstrapSettings(configuredSettings()), cleanRecovery())
	defer store.Close()
	u := &blockingUpdater{started: make(chan context.Context, 1), release: make(chan struct{})}
	executor, err := NewExecutor(service, testconfig.Defaults().Geodata, u)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	if _, err := executor.Execute(context.Background(), "shutdown"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-u.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker not started")
	}
	if err := executor.Close(); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(context.Background())
	if err != nil || status.Operation.State != OperationFailed || status.Operation.ErrorCode != "geodata_interrupted" {
		t.Fatalf("shutdown: %+v %v", status, err)
	}
	if _, err := executor.Execute(context.Background(), "after-close"); !executionErrorIs(err, "geodata_update_unavailable", 503) {
		t.Fatal(err)
	}
}

func TestAsyncFailureIsPolledAndReplayedWithoutRerunning(t *testing.T) {
	store, service := openFixture(t, filepath.Join(t.TempDir(), "state.sqlite"), bootstrapSettings(configuredSettings()), cleanRecovery())
	defer store.Close()
	updater := &reportUpdater{report: api.GeodataReport{Replaced: 1, Issues: []api.GeodataIssue{{Process: "external", File: "geosite.dat", Stage: "checksum", Code: "geodata_external_checksum_failed", Message: "checksum mismatch"}}}}
	executor, err := NewExecutor(service, testconfig.Defaults().Geodata, updater)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	op, err := executor.Execute(context.Background(), "async-failure")
	if err != nil || op.State != OperationRunning {
		t.Fatalf("admission %+v %v", op, err)
	}
	executor.workers.Wait()
	status, err := service.Status(context.Background())
	if err != nil || status.Operation.State != OperationFailed || status.Operation.Report.Replaced != 1 || status.Operation.Report.Issues[0].Stage != "checksum" {
		t.Fatalf("failure %+v %v", status, err)
	}
	replay, err := executor.Execute(context.Background(), "async-failure")
	if err != nil || replay.State != OperationFailed || updater.calls != 1 {
		t.Fatalf("replay %+v %v calls=%d", replay, err, updater.calls)
	}
}
