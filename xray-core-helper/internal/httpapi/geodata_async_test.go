package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/geodataadmin"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type asyncHTTPUpdater struct {
	started chan context.Context
	release chan struct{}
}

func (u *asyncHTTPUpdater) UpdateReportProgress(ctx context.Context, _ bootstrap.GeodataConfig, save func(api.GeodataReport) error) api.GeodataReport {
	if err := save(api.GeodataReport{Process: "external", Stage: "download"}); err != nil {
		panic(err)
	}
	u.started <- ctx
	select {
	case <-u.release:
		return api.GeodataReport{Replaced: 1}
	case <-ctx.Done():
		return api.GeodataReport{Issues: []api.GeodataIssue{{Code: "geodata_interrupted"}}}
	}
}

func TestGeodataHTTPReturnsAcceptedAndSurvivesRequestDisconnect(t *testing.T) {
	contract := httpGeodataService(t, cleanHTTPRecovery())
	updater := &asyncHTTPUpdater{started: make(chan context.Context, 1), release: make(chan struct{})}
	executor, err := geodataadmin.NewExecutor(contract.Service, bootstrap.Defaults().Geodata, updater)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	defer close(updater.release)
	contract.Executor = executor
	server := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{Admin: contract, Executor: contract})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/v1/xray/geodata/update", nil).WithContext(ctx)
	request.Header.Set("Idempotency-Key", "async-http")
	response := httptest.NewRecorder()
	returned := make(chan struct{})
	go func() { server.Handler.ServeHTTP(response, request); close(returned) }()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP waited for updater")
	}
	var accepted api.GeodataUpdateResponse
	if response.Code != 202 || json.Unmarshal(response.Body.Bytes(), &accepted) != nil || accepted.Operation.State != "running" {
		t.Fatalf("admission: %d %s", response.Code, response.Body)
	}
	cancel()
	var workerCtx context.Context
	select {
	case workerCtx = <-updater.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker not started")
	}
	if workerCtx.Err() != nil {
		t.Fatal("request disconnect cancelled worker")
	}
	status := httptest.NewRecorder()
	server.Handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/v1/xray/geodata/update/status", nil))
	var polled api.GeodataStatusResponse
	if status.Code != 200 || json.Unmarshal(status.Body.Bytes(), &polled) != nil || polled.Operation.ID != accepted.Operation.ID || polled.Operation.Report.Stage != "download" {
		t.Fatalf("poll: %s", status.Body)
	}
}
