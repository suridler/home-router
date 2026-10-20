package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/domainservice"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type proxyDomainRuntime struct {
	payloads []xrayconfig.RoutingPayload
	err      error
	calls    int
}

func TestProxyDomainReadsContinueButWritesAreBlockedDuringRecovery(t *testing.T) {
	runtime := &proxyDomainRuntime{}
	handler, _ := newProxyDomainsHandler(t, runtime)
	coordinator.Block()
	t.Cleanup(func() {
		coordinator.Block()
		lease, err := coordinator.AcquireMaintenance(context.Background())
		if err == nil {
			_ = lease.Unblock()
			lease.Release()
		}
	})
	if read := call(handler, http.MethodGet, "/v1/proxy-domains", ""); read.Code != http.StatusOK {
		t.Fatalf("snapshot response = %d %s", read.Code, read.Body.String())
	}
	write := call(handler, http.MethodPost, "/v1/proxy-domains", `{"domain":"blocked.example"}`)
	if write.Code != http.StatusServiceUnavailable || errorCode(t, write) != "proxy_domain_unavailable" ||
		runtime.calls != 0 {
		t.Fatalf("mutation response = %d %s; runtime calls=%d", write.Code, write.Body.String(), runtime.calls)
	}
}

func (runtime *proxyDomainRuntime) ApplyRouting(_ context.Context, payload xrayconfig.RoutingPayload) error {
	runtime.payloads = append(runtime.payloads, append(xrayconfig.RoutingPayload(nil), payload...))
	runtime.calls++
	return runtime.err
}

func newProxyDomainsHandler(t *testing.T, runtime *proxyDomainRuntime) (http.Handler, *domainstore.Store) {
	t.Helper()
	directory := t.TempDir()
	store, err := domainstore.Open(context.Background(), filepath.Join(directory, "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	configPath := filepath.Join(directory, "config.json")
	config := `{"routing":{"rules":[{"ruleTag":"proxy-domains-permanent"},{"ruleTag":"proxy-ip-permanent"}]}}`
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	reader := xrayconfig.NewReader(xrayconfig.Settings{Config: configPath})
	service, err := domainservice.New(store, reader, runtime)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer("test", reader, nil, service).Handler, store
}

func TestProxyResourceHTTPIPv4RoundTripAndValidation(t *testing.T) {
	runtime := &proxyDomainRuntime{}
	handler, store := newProxyDomainsHandler(t, runtime)
	for _, value := range []string{" 192.0.2.1 ", "Example.COM."} {
		body, _ := json.Marshal(api.AddProxyDomainRequest{Domain: value})
		response := call(handler, http.MethodPost, "/v1/proxy-domains", string(body))
		if response.Code != http.StatusOK {
			t.Fatalf("resource rejected: %d %s", response.Code, response.Body.String())
		}
	}
	last := string(runtime.payloads[len(runtime.payloads)-1])
	if !strings.Contains(last, `"ip":["192.0.2.1"]`) || strings.Contains(last, "domain:192.0.2.1") || !strings.Contains(last, "domain:example.com") {
		t.Fatalf("incorrect resource conditions: %s", last)
	}
	before, _ := store.Snapshot(context.Background())
	for _, value := range []string{"999.0.0.1", "192.168.001.1", "192.0.2.1.", "::1", "::ffff:192.0.2.1", "192.0.2.0/24", "https://example.com"} {
		body, _ := json.Marshal(api.AddProxyDomainRequest{Domain: value})
		for _, request := range []struct{ method, path, body string }{
			{http.MethodPost, "/v1/proxy-domains", string(body)},
			{http.MethodDelete, "/v1/proxy-domains/" + url.PathEscape(value), ""},
		} {
			r := call(handler, request.method, request.path, request.body)
			if r.Code != http.StatusBadRequest || errorCode(t, r) != "invalid_domain" {
				t.Fatalf("invalid resource accepted: %d %s", r.Code, r.Body.String())
			}
		}
	}
	after, _ := store.Snapshot(context.Background())
	if runtime.calls != 2 || strings.Join(before.Desired, ",") != strings.Join(after.Desired, ",") {
		t.Fatal("invalid request changed state")
	}
	r := call(handler, http.MethodDelete, "/v1/proxy-domains/192.0.2.1", "")
	if r.Code != http.StatusOK {
		t.Fatalf("delete IPv4: %d %s", r.Code, r.Body.String())
	}
	after, _ = store.Snapshot(context.Background())
	if len(after.Desired) != 1 || after.Desired[0] != "example.com" || strings.Contains(string(runtime.payloads[len(runtime.payloads)-1]), "192.0.2.1") {
		t.Fatal("IPv4 removal not propagated")
	}
}

func TestProxyDomainHTTPContracts(t *testing.T) {
	runtime := &proxyDomainRuntime{}
	handler, store := newProxyDomainsHandler(t, runtime)

	read := call(handler, http.MethodGet, "/v1/proxy-domains", "")
	if read.Code != http.StatusOK || read.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET response = %d %s", read.Code, read.Body.String())
	}
	var snapshot api.ProxyDomainsResponse
	if err := json.Unmarshal(read.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Desired) != 0 || len(snapshot.Baseline) != 0 || snapshot.ConsolidationActive {
		t.Fatalf("initial snapshot = %#v", snapshot)
	}

	add := call(handler, http.MethodPost, "/v1/proxy-domains", `{"domain":" New.Example. "}`)
	if add.Code != http.StatusOK {
		t.Fatalf("POST response = %d %s", add.Code, add.Body.String())
	}
	var mutation api.ProxyDomainMutationResponse
	if err := json.Unmarshal(add.Body.Bytes(), &mutation); err != nil {
		t.Fatal(err)
	}
	if mutation.Domain != "new.example" || !mutation.Applied || runtime.calls != 1 {
		t.Fatalf("add response = %#v; runtime calls = %d", mutation, runtime.calls)
	}

	deleted := call(handler, http.MethodDelete, "/v1/proxy-domains/New.Example.", "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("DELETE response = %d %s", deleted.Code, deleted.Body.String())
	}
	if err := json.Unmarshal(deleted.Body.Bytes(), &mutation); err != nil {
		t.Fatal(err)
	}
	if mutation.Domain != "new.example" || !mutation.Applied || runtime.calls != 2 {
		t.Fatalf("delete response = %#v; runtime calls = %d", mutation, runtime.calls)
	}

	applied := call(handler, http.MethodPost, "/v1/proxy-domains/apply", `{}`)
	if applied.Code != http.StatusOK {
		t.Fatalf("apply response = %d %s", applied.Code, applied.Body.String())
	}
	var applyResponse api.ApplyProxyDomainsResponse
	if err := json.Unmarshal(applied.Body.Bytes(), &applyResponse); err != nil {
		t.Fatal(err)
	}
	if !applyResponse.Applied || runtime.calls != 3 {
		t.Fatalf("apply response = %#v; runtime calls = %d", applyResponse, runtime.calls)
	}
	current, err := store.Snapshot(context.Background())
	if err != nil || len(current.Desired) != 0 {
		t.Fatalf("snapshot after delete = %#v, %v", current, err)
	}
}

func TestProxyDomainHTTPRejectsInvalidRequestsAndBodyOverflow(t *testing.T) {
	runtime := &proxyDomainRuntime{}
	handler, _ := newProxyDomainsHandler(t, runtime)
	requests := []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"missing domain", http.MethodPost, "/v1/proxy-domains", `{}`, http.StatusBadRequest, "invalid_domain"},
		{"invalid domain", http.MethodPost, "/v1/proxy-domains", `{"domain":"https://example.com"}`, http.StatusBadRequest, "invalid_domain"},
		{"unknown field", http.MethodPost, "/v1/proxy-domains", `{"domain":"valid.example","path":"/etc/passwd"}`, http.StatusBadRequest, "invalid_request"},
		{"trailing JSON", http.MethodPost, "/v1/proxy-domains", `{"domain":"valid.example"}{}`, http.StatusBadRequest, "invalid_request"},
		{"non-object JSON", http.MethodPost, "/v1/proxy-domains", `null`, http.StatusBadRequest, "invalid_request"},
		{"delete unknown field", http.MethodDelete, "/v1/proxy-domains/name.example", `{"unexpected":true}`, http.StatusBadRequest, "invalid_request"},
		{"apply unknown field", http.MethodPost, "/v1/proxy-domains/apply", `{"domain":"ignored.example"}`, http.StatusBadRequest, "invalid_request"},
		{"apply trailing JSON", http.MethodPost, "/v1/proxy-domains/apply", `{} false`, http.StatusBadRequest, "invalid_request"},
		{"unknown nested path", http.MethodPost, "/v1/proxy-domains/name.example/extra", "", http.StatusNotFound, "not_found"},
	}
	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			response := call(handler, test.method, test.path, test.body)
			if response.Code != test.status || errorCode(t, response) != test.code {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}

	largeBody := `{"domain":"` + strings.Repeat("a", MaxRequestBodyBytes) + `"}`
	tooLarge := call(handler, http.MethodPost, "/v1/proxy-domains", largeBody)
	if tooLarge.Code != http.StatusRequestEntityTooLarge || errorCode(t, tooLarge) != "request_too_large" {
		t.Fatalf("oversized request = %d %s", tooLarge.Code, tooLarge.Body.String())
	}
	if runtime.calls != 0 {
		t.Fatalf("runtime called for invalid requests: %d", runtime.calls)
	}
}

func TestProxyDomainHTTPMethodAndPathHandling(t *testing.T) {
	handler, _ := newProxyDomainsHandler(t, &proxyDomainRuntime{})
	requests := []struct {
		method, path, allow string
		status              int
		code                string
	}{
		{http.MethodPut, "/v1/proxy-domains", "GET, POST", http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodGet, "/v1/proxy-domains/apply", "POST", http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodGet, "/v1/proxy-domains/name.example", "DELETE", http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodDelete, "/v1/proxy-domains/", "", http.StatusNotFound, "not_found"},
	}
	for _, test := range requests {
		response := call(handler, test.method, test.path, "")
		if response.Code != test.status || errorCode(t, response) != test.code {
			t.Errorf("%s %s response = %d %s", test.method, test.path, response.Code, response.Body.String())
		}
		if test.allow != "" && response.Header().Get("Allow") != test.allow {
			t.Errorf("%s %s Allow = %q, want %q", test.method, test.path, response.Header().Get("Allow"), test.allow)
		}
	}
}

func TestProxyDomainHTTPMapsConsolidationAndPossiblyPersistedErrorsSafely(t *testing.T) {
	t.Run("consolidation conflict", func(t *testing.T) {
		runtime := &proxyDomainRuntime{}
		handler, store := newProxyDomainsHandler(t, runtime)
		if err := store.SetConsolidationActive(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		for _, request := range []struct{ method, path, body string }{
			{http.MethodPost, "/v1/proxy-domains", `{"domain":"blocked.example"}`},
			{http.MethodDelete, "/v1/proxy-domains/blocked.example", ""},
			{http.MethodPost, "/v1/proxy-domains", `{"domain":"192.0.2.1"}`},
			{http.MethodDelete, "/v1/proxy-domains/192.0.2.1", ""},
		} {
			response := call(handler, request.method, request.path, request.body)
			if response.Code != http.StatusConflict || errorCode(t, response) != "consolidation_active" {
				t.Errorf("conflict response = %d %s", response.Code, response.Body.String())
			}
		}
		read := call(handler, http.MethodGet, "/v1/proxy-domains", "")
		var snapshot api.ProxyDomainsResponse
		if err := json.Unmarshal(read.Body.Bytes(), &snapshot); err != nil || !snapshot.ConsolidationActive {
			t.Fatalf("snapshot during consolidation = %#v, %v", snapshot, err)
		}
		if runtime.calls != 0 {
			t.Fatalf("runtime called during consolidation: %d", runtime.calls)
		}
	})

	t.Run("runtime sync failure", func(t *testing.T) {
		runtime := &proxyDomainRuntime{err: errors.New("SECRET raw RoutingService command output")}
		handler, store := newProxyDomainsHandler(t, runtime)
		response := call(handler, http.MethodPost, "/v1/proxy-domains", `{"domain":"persisted.example"}`)
		if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != domainservice.RuntimeSyncFailedCode {
			t.Fatalf("runtime failure response = %d %s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "SECRET") || strings.Contains(response.Body.String(), "RoutingService command output") {
			t.Fatalf("runtime details leaked: %s", response.Body.String())
		}
		snapshot, err := store.Snapshot(context.Background())
		if err != nil || len(snapshot.Desired) != 1 || snapshot.Desired[0] != "persisted.example" {
			t.Fatalf("possibly persisted state = %#v, %v", snapshot, err)
		}

		apply := call(handler, http.MethodPost, "/v1/proxy-domains/apply", "")
		if apply.Code != http.StatusServiceUnavailable || errorCode(t, apply) != "domain_runtime_apply_failed" ||
			strings.Contains(apply.Body.String(), "SECRET") {
			t.Fatalf("apply failure response = %d %s", apply.Code, apply.Body.String())
		}
	})
}
