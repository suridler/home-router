package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/unixhttp"
	"home-router/xray-core-helper/internal/xrayconfig"
)

func TestHealthContractOverTemporaryUnixSocket(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "xch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "helper.sock")
	listener, err := unixhttp.Listen(socketPath, "")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer("test-version", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil)
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		_ = listener.Close()
		<-serveResult
	})

	client := unixhttp.NewClient(socketPath, time.Second)
	response, err := client.Get("http://helper/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}
	var payload api.HealthResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "ok" || payload.Version != "test-version" {
		t.Fatalf("health payload = %#v", payload)
	}
}

func TestServerWriteTimeoutCoversBoundedLongOperations(t *testing.T) {
	server := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil)
	if server.WriteTimeout != 20*time.Minute {
		t.Fatalf("WriteTimeout = %s; want 20m", server.WriteTimeout)
	}
	if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 10*time.Second || server.IdleTimeout != 30*time.Second {
		t.Fatalf("short request timeouts changed: header=%s read=%s idle=%s",
			server.ReadHeaderTimeout, server.ReadTimeout, server.IdleTimeout)
	}
}

type readinessFake struct {
	response api.ReadinessResponse
}

func (fake readinessFake) Check(context.Context) api.ReadinessResponse { return fake.response }

func TestReadinessHTTPContract(t *testing.T) {
	tests := []struct {
		name       string
		readiness  ReadinessService
		wantStatus int
		wantCode   string
	}{
		{
			name: "ready",
			readiness: readinessFake{response: api.ReadinessResponse{Status: "ready", Checks: []api.ReadinessCheck{
				{Name: "config", Status: "ready", Code: "config_ready"},
			}}},
			wantStatus: http.StatusOK,
			wantCode:   "config_ready",
		},
		{
			name: "degraded",
			readiness: readinessFake{response: api.ReadinessResponse{Status: "degraded", Checks: []api.ReadinessCheck{
				{Name: "xray_api", Status: "degraded", Code: "xray_api_unavailable"},
			}}},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "xray_api_unavailable",
		},
		{name: "unavailable dependency", wantStatus: http.StatusServiceUnavailable, wantCode: "readiness_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			server := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil,
				ServerServices{Readiness: test.readiness})
			server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/ready", nil))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var payload api.ReadinessResponse
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Checks) != 1 || payload.Checks[0].Code != test.wantCode {
				t.Fatalf("payload = %#v", payload)
			}
		})
	}

	methodResponse := httptest.NewRecorder()
	NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil).Handler.ServeHTTP(
		methodResponse, httptest.NewRequest(http.MethodPost, "/v1/ready", nil))
	if methodResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", methodResponse.Code)
	}
}

func TestUnknownEndpointReturnsStructuredError(t *testing.T) {
	responseRecorder := httptest.NewRecorder()
	NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil).Handler.ServeHTTP(responseRecorder, httptest.NewRequest(http.MethodGet, "/v1/unknown", nil))
	if responseRecorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", responseRecorder.Code)
	}
	var payload api.ErrorResponse
	if err := json.Unmarshal(responseRecorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "not_found" {
		t.Fatalf("error code = %q", payload.Error.Code)
	}
}

func TestXrayReadEndpointsAndSafeErrors(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"inbounds":[],"outbounds":[],"routing":{},"log":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	reader := xrayconfig.NewReader(xrayconfig.Settings{
		Binary:   filepath.Join(t.TempDir(), "xray"),
		Config:   configPath,
		API:      "127.0.0.1:10085",
		AssetDir: t.TempDir(),
	})
	server := NewServer("test", reader, nil, nil)
	paths := []string{
		"/v1/xray/runtime", "/v1/xray/exporter-metadata", "/v1/xray/status", "/v1/xray/config/summary", "/v1/xray/inbounds",
		"/v1/xray/outbounds", "/v1/xray/routing", "/v1/xray/logging", "/v1/xray/geodata",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
			}
			if path == "/v1/xray/status" {
				var status api.StatusResponse
				if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
					t.Fatal(err)
				}
				if status.ConfigState != "parsed" || !status.ConfigReadable || !status.ConfigParsed {
					t.Fatalf("status = %#v", status)
				}
			}
		})
	}

	methodResponse := httptest.NewRecorder()
	server.Handler.ServeHTTP(methodResponse, httptest.NewRequest(http.MethodPost, "/v1/xray/outbounds", nil))
	if methodResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", methodResponse.Code)
	}

	badConfigPath := filepath.Join(t.TempDir(), "bad-config.json")
	if err := os.WriteFile(badConfigPath, []byte(`{"password":"secret", /* unfinished`), 0600); err != nil {
		t.Fatal(err)
	}
	badReader := xrayconfig.NewReader(xrayconfig.Settings{Config: badConfigPath})
	badResponse := httptest.NewRecorder()
	NewServer("test", badReader, nil, nil).Handler.ServeHTTP(badResponse, httptest.NewRequest(http.MethodGet, "/v1/xray/config/summary", nil))
	if badResponse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed config status = %d, body = %s", badResponse.Code, badResponse.Body.String())
	}
	var errorPayload api.ErrorResponse
	if err := json.Unmarshal(badResponse.Body.Bytes(), &errorPayload); err != nil {
		t.Fatal(err)
	}
	if errorPayload.Error.Code != "invalid_config" || strings.Contains(badResponse.Body.String(), "secret") {
		t.Fatalf("malformed config error = %s", badResponse.Body.String())
	}
}

func TestExporterMetadataContractIsCredentialFree(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	config := `{
		"inbounds":[
			{"tag":"tproxy-in","protocol":"dokodemo-door","settings":{"password":"inbound-secret"}},
			{"tag":"tproxy-in"},
			{"protocol":"socks"}
		],
		"outbounds":[
			{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"users":[{"id":"11111111-1111-1111-1111-111111111111"}]}]}},
			{"tag":"direct","protocol":"freedom"},
			{"tag":"proxy","settings":{"password":"outbound-secret"}}
		]
	}`
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	reader := xrayconfig.NewReader(xrayconfig.Settings{Config: configPath, API: "127.0.0.1:10085"})
	server := NewServer("test", reader, nil, nil)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/xray/exporter-metadata", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var metadata api.ExporterMetadataResponse
	if err := json.Unmarshal(response.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.API != "127.0.0.1:10085" || metadata.Generation != 0 || metadata.ContentHash == "" || metadata.ModifiedAt.IsZero() {
		t.Fatalf("metadata = %#v", metadata)
	}
	if got := strings.Join(metadata.InboundTags, ","); got != "tproxy-in" {
		t.Fatalf("inbound tags = %q", got)
	}
	if got := strings.Join(metadata.OutboundTags, ","); got != "proxy,direct" {
		t.Fatalf("outbound tags = %q", got)
	}
	body := response.Body.String()
	for _, secret := range []string{"inbound-secret", "outbound-secret", "11111111-1111-1111-1111-111111111111", "settings", "protocol", "vnext", "users"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response exposed %q: %s", secret, body)
		}
	}

	methodResponse := httptest.NewRecorder()
	server.Handler.ServeHTTP(methodResponse, httptest.NewRequest(http.MethodPost, "/v1/xray/exporter-metadata", nil))
	if methodResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", methodResponse.Code)
	}
}

func TestConfigAdoptRejectsStaleGenerationAndAcceptsValidatedDrift(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{"inbounds":[],"outbounds":[],"routing":{}}`)
	if err := os.WriteFile(configPath, original, 0640); err != nil {
		t.Fatal(err)
	}
	store, err := transaction.OpenStore(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	engine, err := transaction.NewEngine(configPath, store, acceptConfigValidator{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader := xrayconfig.NewReader(xrayconfig.Settings{Config: configPath}, store)
	server := NewServer("test", reader, engine, nil)
	beforeInfo, _ := os.Stat(configPath)
	staleResponse := httptest.NewRecorder()
	server.Handler.ServeHTTP(staleResponse, httptest.NewRequest(http.MethodPost, "/v1/xray/config/adopt", strings.NewReader(`{"expectedGeneration":3}`)))
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf("stale adopt status = %d, body = %s", staleResponse.Code, staleResponse.Body.String())
	}
	var conflict api.ErrorResponse
	if err := json.Unmarshal(staleResponse.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Error.Code != "generation_conflict" {
		t.Fatalf("stale adopt error = %#v", conflict.Error)
	}
	afterBytes, _ := os.ReadFile(configPath)
	afterInfo, _ := os.Stat(configPath)
	if !bytes.Equal(afterBytes, original) || !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatal("stale adopt touched the configuration file")
	}

	for _, body := range []string{`{}`, `{"expectedGeneration":0,"path":"/tmp/other.json"}`} {
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/xray/config/adopt", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid request %s status = %d", body, response.Code)
		}
	}

	external := []byte(`{"inbounds":[],"outbounds":[],"routing":{"domainStrategy":"AsIs"}}`)
	if err := os.WriteFile(configPath, external, 0640); err != nil {
		t.Fatal(err)
	}
	adoptResponse := httptest.NewRecorder()
	server.Handler.ServeHTTP(adoptResponse, httptest.NewRequest(http.MethodPost, "/v1/xray/config/adopt", strings.NewReader(`{"expectedGeneration":0}`)))
	if adoptResponse.Code != http.StatusOK {
		t.Fatalf("adopt status = %d, body = %s", adoptResponse.Code, adoptResponse.Body.String())
	}
	var adopted api.AdoptConfigResponse
	if err := json.Unmarshal(adoptResponse.Body.Bytes(), &adopted); err != nil {
		t.Fatal(err)
	}
	if !adopted.Adopted || adopted.Generation != 1 {
		t.Fatalf("adopt response = %#v", adopted)
	}
	if adopted.ContentHash == "" {
		t.Fatalf("adopt response hash = %q", adopted.ContentHash)
	}
}

type acceptConfigValidator struct{}

func (acceptConfigValidator) Validate(context.Context, string) error { return nil }

func TestRequestBodyLimitAppliesToUnknownLengthBodies(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/health", bytes.NewReader(make([]byte, MaxRequestBodyBytes+1)))
	request.ContentLength = -1
	responseRecorder := httptest.NewRecorder()
	NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil).Handler.ServeHTTP(responseRecorder, request)
	if responseRecorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d; want %d", responseRecorder.Code, http.StatusRequestEntityTooLarge)
	}
	var payload api.ErrorResponse
	if err := json.Unmarshal(responseRecorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "request_too_large" {
		t.Fatalf("error code = %q", payload.Error.Code)
	}
}
