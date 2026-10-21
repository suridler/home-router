package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"home-router/xray-core-helper/internal/testconfig"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/geodataadmin"
	"home-router/xray-core-helper/internal/geodatacontract"
	"home-router/xray-core-helper/internal/geodatarecovery"
	"home-router/xray-core-helper/internal/systemsettings"
	"home-router/xray-core-helper/internal/xrayconfig"
	"os"
)

func TestGeodataSettingsStrictContractAndGeneration(t *testing.T) {
	valid := `{"expectedGeneration":0,"settings":{"origins":["https://data.example"],"geosite":{"url":"https://data.example/site.dat","checksum":{"url":"https://data.example/site.sha256","algorithm":"sha256"}},"geoip":{"url":"https://data.example/ip.dat","checksum":{"url":"https://data.example/ip.sha256","algorithm":"sha256"}}}}`
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "top-level duplicate", body: `{"expectedGeneration":0,"expectedGeneration":0,"settings":{}}`, want: http.StatusBadRequest},
		{name: "nested duplicate", body: `{"expectedGeneration":0,"settings":{"origins":[],"origins":[]}}`, want: http.StatusBadRequest},
		{name: "unknown field", body: `{"expectedGeneration":0,"settings":{},"unknown":true}`, want: http.StatusBadRequest},
		{name: "trailing JSON", body: valid + `{}`, want: http.StatusBadRequest},
		{name: "null generation", body: strings.Replace(valid, `"expectedGeneration":0`, `"expectedGeneration":null`, 1), want: http.StatusBadRequest},
		{name: "missing generation", body: `{"settings":{}}`, want: http.StatusBadRequest},
		{name: "null settings", body: `{"expectedGeneration":0,"settings":null}`, want: http.StatusBadRequest},
		{name: "null value", body: strings.Replace(valid, `"origins":["https://data.example"]`, `"origins":null`, 1), want: http.StatusBadRequest},
		{name: "not an object", body: `null`, want: http.StatusBadRequest},
		{name: "oversized", body: `{"expectedGeneration":0,"settings":{}}` + strings.Repeat(" ", MaxRequestBodyBytes), want: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := httpGeodataService(t, cleanHTTPRecovery())
			server := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil,
				ServerServices{Admin: service})
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
				"/v1/xray/geodata/settings", strings.NewReader(test.body)))
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}

	service := httpGeodataService(t, cleanHTTPRecovery())
	server := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{Admin: service})
	get := httptest.NewRecorder()
	server.Handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/xray/geodata/settings", nil))
	var settings api.VersionedGeodataSettings
	if get.Code != http.StatusOK || json.Unmarshal(get.Body.Bytes(), &settings) != nil {
		t.Fatalf("settings GET status=%d body=%s", get.Code, get.Body.String())
	}
	valid = strings.Replace(valid, `"expectedGeneration":0`, fmt.Sprintf(`"expectedGeneration":%d`, settings.Generation), 1)
	put := httptest.NewRecorder()
	server.Handler.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/v1/xray/geodata/settings", strings.NewReader(valid)))
	if put.Code != http.StatusOK {
		t.Fatalf("settings PUT status=%d body=%s", put.Code, put.Body.String())
	}
	stale := httptest.NewRecorder()
	server.Handler.ServeHTTP(stale, httptest.NewRequest(http.MethodPut, "/v1/xray/geodata/settings", strings.NewReader(valid)))
	var conflict api.ErrorResponse
	if stale.Code != http.StatusConflict || json.Unmarshal(stale.Body.Bytes(), &conflict) != nil ||
		conflict.Error.Code != "generation_conflict" {
		t.Fatalf("stale update status=%d body=%s", stale.Code, stale.Body.String())
	}
}

func TestGeodataStatusAndUpdateHTTPContracts(t *testing.T) {
	service := httpGeodataService(t, cleanHTTPRecovery())
	executor := &httpUpdateFake{operation: api.GeodataOperation{
		ID: "0123456789abcdef0123456789abcdef", SettingsGeneration: 0,
		State: string(geodataadmin.OperationSucceeded), ResultCode: "geodata_update_succeeded",
	}}
	server := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil,
		ServerServices{Admin: service, Executor: executor})

	status := httptest.NewRecorder()
	server.Handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/v1/xray/geodata/update/status", nil))
	var projection api.GeodataStatusResponse
	if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &projection) != nil ||
		projection.Recovery.Phase != "clean" || projection.Operation != nil {
		t.Fatalf("status response=%d %s", status.Code, status.Body.String())
	}

	for _, key := range []string{"", "has space", strings.Repeat("k", 129)} {
		request := httptest.NewRequest(http.MethodPost, "/v1/xray/geodata/update", nil)
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid key %q status=%d", key, response.Code)
		}
	}
	duplicateKey := httptest.NewRequest(http.MethodPost, "/v1/xray/geodata/update", nil)
	duplicateKey.Header.Add("Idempotency-Key", "first")
	duplicateKey.Header.Add("Idempotency-Key", "second")
	duplicateResponse := httptest.NewRecorder()
	server.Handler.ServeHTTP(duplicateResponse, duplicateKey)
	if duplicateResponse.Code != http.StatusBadRequest {
		t.Errorf("duplicate Idempotency-Key status=%d", duplicateResponse.Code)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/xray/geodata/update", nil)
	request.Header.Set("Idempotency-Key", "key-1")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	var update api.GeodataUpdateResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &update) != nil ||
		update.Operation.State != string(geodataadmin.OperationSucceeded) ||
		executor.calls != 1 || executor.key != "key-1" {
		t.Fatalf("update response=%d %s calls=%d key=%q", response.Code, response.Body.String(), executor.calls, executor.key)
	}

	unavailable := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil,
		ServerServices{Admin: service, Executor: service})
	request = httptest.NewRequest(http.MethodPost, "/v1/xray/geodata/update", nil)
	request.Header.Set("Idempotency-Key", "key-2")
	response = httptest.NewRecorder()
	unavailable.Handler.ServeHTTP(response, request)
	var unavailableError api.ErrorResponse
	if response.Code != http.StatusServiceUnavailable ||
		json.Unmarshal(response.Body.Bytes(), &unavailableError) != nil ||
		unavailableError.Error.Code != "geodata_update_unavailable" {
		t.Fatalf("unavailable response=%d %s", response.Code, response.Body.String())
	}
}

type httpUpdateFake struct {
	calls     int
	key       string
	operation api.GeodataOperation
	err       error
}

func TestGeodataOversizeResponseNamesConfiguredLimit(t *testing.T) {
	executor := &httpUpdateFake{err: &api.ContractError{Code: "geodata_asset_too_large", HTTPStatus: 503, MaxAssetSizeMB: 512}}
	request := httptest.NewRequest(http.MethodPost, "/v1/xray/geodata/update", nil)
	request.Header.Set("Idempotency-Key", "size-test")
	response := httptest.NewRecorder()
	postGeodataUpdate(executor)(response, request)
	var body api.ErrorResponse
	if response.Code != 503 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Error.MaxAssetSizeMB != 512 ||
		body.Error.Code != "geodata_asset_too_large" || !strings.Contains(body.Error.Message, "превышает заданный лимит — 512 МБ") {
		t.Fatal(response.Code, response.Body.String())
	}
}

func (fake *httpUpdateFake) Execute(_ context.Context, key string) (api.GeodataOperation, error) {
	fake.calls++
	fake.key = key
	return fake.operation, fake.err
}

func httpGeodataService(t *testing.T, recovery geodatarecovery.Status) geodatacontract.Contract {
	t.Helper()
	initial := bootstrap.GeodataConfig{
		Geosite: bootstrap.GeodataSourceConfig{Checksum: bootstrap.GeodataChecksumConfig{Algorithm: "sha256"}},
		Geoip:   bootstrap.GeodataSourceConfig{Checksum: bootstrap.GeodataChecksumConfig{Algorithm: "sha256"}},
	}
	store, err := geodataadmin.Open(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"), initial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := testconfig.Defaults()
	cfg.Geodata.Geosite = initial.Geosite
	cfg.Geodata.Geoip = initial.Geoip
	cfg.Geodata.Origins = []string{}
	path := filepath.Join(t.TempDir(), "config.toml")
	encoded, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := systemsettings.New(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	service, err := geodataadmin.NewService(geodataadmin.ServiceOptions{
		Store: store, Settings: settings, Recovery: httpRecovery{status: recovery},
	})
	if err != nil {
		t.Fatal(err)
	}
	return geodatacontract.Contract{Service: service}
}

type httpRecovery struct{ status geodatarecovery.Status }

func (recovery httpRecovery) Status() geodatarecovery.Status { return recovery.status }

func cleanHTTPRecovery() geodatarecovery.Status {
	return geodatarecovery.Status{
		Code: geodatarecovery.RecoveryNone.String(),
		Result: geodatarecovery.Result{
			State: geodatarecovery.State{Phase: geodatarecovery.PhaseClean},
			Plan:  geodatarecovery.Plan{Phase: geodatarecovery.PhaseClean},
		},
	}
}

func TestGeodataStatusUnavailableIsRedacted(t *testing.T) {
	server := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/xray/geodata/update/status", nil))
	var payload api.ErrorResponse
	if response.Code != http.StatusServiceUnavailable ||
		json.Unmarshal(response.Body.Bytes(), &payload) != nil ||
		payload.Error.Code != "geodata_status_unavailable" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), fmt.Sprint("/private")) {
		t.Fatal("response disclosed internal state")
	}
}

func TestGeodataSettingsHTTPMapsLiveRecoveryBlockToUnavailable(t *testing.T) {
	service := httpGeodataService(t, cleanHTTPRecovery())
	coordinator.Block()
	t.Cleanup(func() {
		lease, err := coordinator.AcquireMaintenance(context.Background())
		if err != nil {
			t.Fatalf("acquire coordinator reset lease: %v", err)
		}
		if err := lease.Unblock(); err != nil {
			lease.Release()
			t.Fatalf("reset coordinator: %v", err)
		}
		lease.Release()
	})
	server := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil,
		ServerServices{Admin: service})
	body := `{"expectedGeneration":0,"settings":{"origins":["https://data.example"],"geosite":{"url":"https://data.example/site.dat","checksum":{"url":"https://data.example/site.sha256","algorithm":"sha256"}},"geoip":{"url":"https://data.example/ip.dat","checksum":{"url":"https://data.example/ip.sha256","algorithm":"sha256"}}}}`
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/v1/xray/geodata/settings", strings.NewReader(body)))
	var payload api.ErrorResponse
	if response.Code != http.StatusServiceUnavailable ||
		json.Unmarshal(response.Body.Bytes(), &payload) != nil ||
		payload.Error.Code != "geodata_recovery_required" {
		t.Fatalf("blocked settings PUT status=%d body=%s", response.Code, response.Body.String())
	}
	settings, err := service.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("settings after blocked PUT=%#v err=%v", settings, err)
	}
}
