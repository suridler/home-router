package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/xrayconfig"
)

const mutationFixture = `{
  "inbounds": [
    { "tag": "tproxy-in", "protocol": "dokodemo-door", "settings": { "followRedirect": true } }
  ],
  "outbounds": [
    { "tag": "proxy", "protocol": "vless", "streamSettings":{"sockopt":{"mark":255}} },
    { "tag": "direct", "protocol": "freedom" },
    { "tag": "spare", "protocol": "freedom" }
  ],
  "routing": {
    "rules": [
      { "ruleTag": "gateway", "inboundTag": ["tproxy-in"], "outboundTag": "proxy" },
      { "ruleTag": "user a", "domain": ["a.example"], "outboundTag": "direct" },
      { "ruleTag": "user-b", "domain": ["b.example"], "outboundTag": "direct" }
    ]
  }
}`

const mutationURI = "vless://33333333-3333-4333-8333-333333333333@edge.example:443?type=tcp&security=reality&pbk=secret-public-key&sni=s.example&sid=feedbeef"

func newMutationServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	return newMutationServerWith(t, acceptConfigValidator{})
}

type rejectConfigValidator struct{}

func (rejectConfigValidator) Validate(context.Context, string) error {
	return errors.New("rejected by test validator")
}

func newMutationServerWith(t *testing.T, validator transaction.Validator) (http.Handler, string) {
	t.Helper()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte(mutationFixture), 0640); err != nil {
		t.Fatal(err)
	}
	store, err := transaction.OpenStore(context.Background(), filepath.Join(directory, "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	engine, err := transaction.NewEngine(configPath, store, validator, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader := xrayconfig.NewReader(xrayconfig.Settings{Config: configPath}, store)
	return NewServer("test", reader, engine, nil).Handler, configPath
}

func TestServerRemainsReadOnlyAndServesHealthWhileRecoveryBlocksMutations(t *testing.T) {
	handler, _ := newMutationServer(t)
	coordinator.Block()
	t.Cleanup(func() {
		coordinator.Block()
		lease, err := coordinator.AcquireMaintenance(context.Background())
		if err == nil {
			_ = lease.Unblock()
			lease.Release()
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Get(server.URL + "/v1/health")
	if err != nil {
		t.Fatalf("read-only health request failed: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health response = %d; want 200", response.StatusCode)
	}
	mutation := call(handler, http.MethodPost, "/v1/xray/outbound-drafts/apply",
		`{"expectedGeneration":1,"outbound":{"tag":"added","protocol":"freedom"}}`)
	if mutation.Code != http.StatusServiceUnavailable ||
		errorCode(t, mutation) != "transaction_unavailable" {
		t.Fatalf("mutation response = %d %s", mutation.Code, mutation.Body.String())
	}
}

func call(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
	return response
}

func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var payload api.ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body is not an error envelope: %s", response.Body.String())
	}
	return payload.Error.Code
}

func generationOf(t *testing.T, response *httptest.ResponseRecorder) uint64 {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload api.MutationResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Generation
}

func TestOutboundImportIsDraftOnlyAndErrorsDoNotLeak(t *testing.T) {
	handler, path := newMutationServer(t)
	before, _ := os.ReadFile(path)
	uri := "vless://33333333-3333-4333-8333-333333333333@edge.example:443?type=tcp&security=tls&unknown=ignored"
	body, _ := json.Marshal(api.OutboundImportRequest{URI: uri})
	response := call(handler, http.MethodPost, "/v1/xray/outbound-drafts/import", string(body))
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	var payload api.OutboundImportResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || len(payload.Warnings) != 1 || !strings.Contains(string(payload.Outbound), "33333333") {
		t.Fatal("selected draft missing credentials/warning")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("import mutated config")
	}
	bad := call(handler, http.MethodPost, "/v1/xray/outbound-drafts/import", `{"uri":"unknown://private-password"}`)
	if bad.Code != 400 || strings.Contains(bad.Body.String(), "private-password") {
		t.Fatal("unsafe import error")
	}
	selected := call(handler, http.MethodGet, "/v1/xray/outbounds/proxy", "")
	if selected.Code != 200 || selected.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("selected editor unavailable")
	}
	list := call(handler, http.MethodGet, "/v1/xray/outbounds", "")
	if strings.Contains(list.Body.String(), "11111111-1111") {
		t.Fatal("list leaked credentials")
	}
}

func TestOutboundEndpointsEnforceGenerationAndProtection(t *testing.T) {
	handler, configPath := newMutationServer(t)
	add, _ := json.Marshal(api.OutboundWriteRequest{MutationRequest: api.MutationRequest{ExpectedGeneration: new(uint64)}, Outbound: json.RawMessage(`{"tag":"edge-2","protocol":"freedom"}`)})
	if generation := generationOf(t, call(handler, http.MethodPost, "/v1/xray/outbound-drafts/apply", string(add))); generation != 1 {
		t.Fatalf("generation = %d", generation)
	}
	if stale := call(handler, http.MethodPost, "/v1/xray/outbound-drafts/apply", string(add)); stale.Code != http.StatusConflict || errorCode(t, stale) != "generation_conflict" {
		t.Fatalf("stale add = %d %s", stale.Code, stale.Body.String())
	}
	before, _ := os.ReadFile(configPath)
	tests := []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"missing generation", http.MethodDelete, "/v1/xray/outbounds/spare", `{}`, 400, "invalid_request"},
		{"unknown field", http.MethodDelete, "/v1/xray/outbounds/spare", `{"expectedGeneration":1,"path":"/etc"}`, 400, "invalid_request"},
		{"trailing data", http.MethodDelete, "/v1/xray/outbounds/spare", `{"expectedGeneration":1}{}`, 400, "invalid_request"},
		{"protected delete", http.MethodDelete, "/v1/xray/outbounds/proxy", `{"expectedGeneration":1}`, 403, "outbound_protected"},
		{"protected tag delete", http.MethodDelete, "/v1/xray/outbounds/direct", `{"expectedGeneration":1}`, 403, "outbound_protected"},
		{"update missing", http.MethodPut, "/v1/xray/outbounds/ghost", `{"expectedGeneration":1,"outbound":{"protocol":"freedom"}}`, 404, "outbound_not_found"},
		{"wrong method", http.MethodPatch, "/v1/xray/outbounds/spare", ``, 405, "method_not_allowed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := call(handler, test.method, test.path, test.body)
			if response.Code != test.status || errorCode(t, response) != test.code {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Fatal("rejected requests changed the configuration")
	}
	update := `{"expectedGeneration":1,"outbound":{"protocol":"vless","settings":{"address":"new.example","port":443,"id":"33333333-3333-4333-8333-333333333333"}}}`
	if generation := generationOf(t, call(handler, http.MethodPut, "/v1/xray/outbounds/proxy", update)); generation != 2 {
		t.Fatalf("update generation = %d", generation)
	}
	if generation := generationOf(t, call(handler, http.MethodDelete, "/v1/xray/outbounds/spare", `{"expectedGeneration":2}`)); generation != 3 {
		t.Fatalf("delete generation = %d", generation)
	}
}

func TestRuleEndpointsUseEscapedRuleTagAndProtectGatewayRules(t *testing.T) {
	handler, configPath := newMutationServer(t)
	rule := `{"ruleTag":"user a","conditions":{"domain":["new.example"]},"outboundTag":"direct"}`
	if generation := generationOf(t, call(handler, http.MethodPut, "/v1/xray/routing/rules/user%20a", `{"expectedGeneration":0,"rule":`+rule+`}`)); generation != 1 {
		t.Fatalf("edit generation = %d", generation)
	}
	if generation := generationOf(t, call(handler, http.MethodPost, "/v1/xray/routing/rules/user%20a/move", `{"expectedGeneration":1,"direction":"down"}`)); generation != 2 {
		t.Fatalf("move generation = %d", generation)
	}
	added := `{"expectedGeneration":2,"position":1,"rule":{"ruleTag":"first","conditions":{"ip":["10.0.0.0/8"]},"outboundTag":"direct"}}`
	if generation := generationOf(t, call(handler, http.MethodPost, "/v1/xray/routing/rules", added)); generation != 3 {
		t.Fatalf("add generation = %d", generation)
	}
	if generation := generationOf(t, call(handler, http.MethodPost, "/v1/xray/routing/rules/clone", `{"expectedGeneration":3,"position":3,"ruleTag":"copy-b"}`)); generation != 4 {
		t.Fatalf("clone generation = %d", generation)
	}
	if call(handler, http.MethodDelete, "/v1/xray/routing/rules/gateway", `{"expectedGeneration":4}`).Code != http.StatusForbidden {
		t.Fatal("protected rule delete was not rejected")
	}
	before, _ := os.ReadFile(configPath)
	tests := []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"protected edit", http.MethodPut, "/v1/xray/routing/rules/gateway", `{"expectedGeneration":4,"rule":` + rule + `}`, 403, "rule_protected"},
		{"protected move", http.MethodPost, "/v1/xray/routing/rules/gateway/move", `{"expectedGeneration":4,"direction":"down"}`, 403, "rule_protected"},
		{"clone protected", http.MethodPost, "/v1/xray/routing/rules/clone", `{"expectedGeneration":4,"position":2,"ruleTag":"x"}`, 403, "rule_protected"},
		{"missing rule", http.MethodDelete, "/v1/xray/routing/rules/ghost", `{"expectedGeneration":4}`, 404, "rule_not_found"},
		{"stale", http.MethodDelete, "/v1/xray/routing/rules/user-b", `{"expectedGeneration":1}`, 409, "generation_conflict"},
		{"wrong method", http.MethodGet, "/v1/xray/routing/rules", ``, 405, "method_not_allowed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := call(handler, test.method, test.path, test.body)
			if response.Code != test.status || errorCode(t, response) != test.code {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Fatal("rejected requests changed the configuration")
	}
	if generation := generationOf(t, call(handler, http.MethodDelete, "/v1/xray/routing/rules/user-b", `{"expectedGeneration":4}`)); generation != 5 {
		t.Fatalf("delete generation = %d", generation)
	}
}

func TestValidationFailureLeavesConfigurationByteIdentical(t *testing.T) {
	handler, configPath := newMutationServerWith(t, rejectConfigValidator{})
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	infoBefore, _ := os.Stat(configPath)
	rule := `{"ruleTag":"user a","conditions":{"domain":["new.example"]},"outboundTag":"direct"}`
	requests := []struct{ name, method, path, body string }{
		{"add outbound", http.MethodPost, "/v1/xray/outbound-drafts/apply", `{"expectedGeneration":0,"outbound":{"tag":"edge-2","protocol":"freedom"}}`},
		{"update outbound", http.MethodPut, "/v1/xray/outbounds/proxy", `{"expectedGeneration":0,"outbound":{"protocol":"vless","settings":{"address":"new.example","port":443}}}`},
		{"delete outbound", http.MethodDelete, "/v1/xray/outbounds/spare", `{"expectedGeneration":0}`},
		{"add rule", http.MethodPost, "/v1/xray/routing/rules", `{"expectedGeneration":0,"rule":{"ruleTag":"new","conditions":{"domain":["n.example"]},"outboundTag":"direct"}}`},
		{"edit rule", http.MethodPut, "/v1/xray/routing/rules/user%20a", `{"expectedGeneration":0,"rule":` + rule + `}`},
		{"move rule", http.MethodPost, "/v1/xray/routing/rules/user%20a/move", `{"expectedGeneration":0,"direction":"down"}`},
		{"delete rule", http.MethodDelete, "/v1/xray/routing/rules/user-b", `{"expectedGeneration":0}`},
	}
	for _, request := range requests {
		t.Run(request.name, func(t *testing.T) {
			response := call(handler, request.method, request.path, request.body)
			if response.Code != http.StatusUnprocessableEntity || errorCode(t, response) != "validation_failed" {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "rejected by test validator") {
				t.Fatal("validator output leaked to the client")
			}
			after, _ := os.ReadFile(configPath)
			if string(after) != string(before) {
				t.Fatal("configuration bytes changed")
			}
			infoAfter, _ := os.Stat(configPath)
			if infoAfter.Mode() != infoBefore.Mode() {
				t.Fatalf("mode = %v; want %v", infoAfter.Mode(), infoBefore.Mode())
			}
			leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(configPath), ".config.json.xch-*"))
			if len(leftovers) != 0 {
				t.Fatalf("artifacts left behind: %v", leftovers)
			}
		})
	}
	status := call(handler, http.MethodGet, "/v1/xray/status", ``)
	var payload api.StatusResponse
	if err := json.Unmarshal(status.Body.Bytes(), &payload); err != nil || payload.Generation != 0 || payload.ConfigDrift {
		t.Fatalf("status after failures = %#v, error = %v", payload, err)
	}
}

func TestMutationEndpointsWithoutServiceAreUnavailable(t *testing.T) {
	handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil).Handler
	response := call(handler, http.MethodDelete, "/v1/xray/outbounds/spare", `{"expectedGeneration":0}`)
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "transaction_unavailable" {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}
