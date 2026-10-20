package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"context"
	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/safeapply"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type readyForRebootTest struct{}

func (readyForRebootTest) Check(context.Context) api.ReadinessResponse {
	return api.ReadinessResponse{Status: "ready"}
}

func TestRebootRequiredBlocksWritesButRetainsReadAccess(t *testing.T) {
	fake := &applyTemplateFake{transaction: transactionFixture()}
	fake.transaction.Phase = safeapply.PhaseAwaitingReboot
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		called := false
		handler := rebootGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }), fake)
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, httptest.NewRequest(method, "/v1/xray/config/adopt", nil))
		if called || result.Code != 409 || !strings.Contains(result.Body.String(), "reboot_required") {
			t.Fatal("reboot gate bypassed", result.Code)
		}
	}
	for _, test := range []struct{ method, path string }{{"POST", "/v1/errors/collect"}, {"GET", "/v1/health"}, {"POST", "/v1/templates/refresh"}, {"POST", "/v1/safe-apply/tx-1/rollback"}} {
		called := false
		handler := rebootGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }), fake)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(test.method, test.path, nil))
		if !called {
			t.Fatal("read/recovery operation blocked", test.path)
		}
	}
}

func TestReadinessDistinguishesFileRestoreFromRuntimeRecovery(t *testing.T) {
	fake := &applyTemplateFake{transaction: transactionFixture()}
	fake.transaction.Phase = safeapply.PhaseAwaitingReboot
	handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{ApplyTemplate: fake, Readiness: readyForRebootTest{}}).Handler
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/v1/ready", nil))
	if response.Code != 503 || !strings.Contains(response.Body.String(), `"code":"reboot_required"`) {
		t.Fatalf("false readiness: %d %s", response.Code, response.Body.String())
	}
}

func TestPartialRecoveryBlocksWritesWithoutClaimingRebootReady(t *testing.T) {
	fake := &applyTemplateFake{transaction: transactionFixture()}
	fake.transaction.Phase = safeapply.PhaseRollingBack
	handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{ApplyTemplate: fake, Readiness: readyForRebootTest{}}).Handler
	for _, path := range []string{"/v1/xray/config/adopt", "/v1/templates/sysctl/apply"} {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest("POST", path, strings.NewReader(`{}`)))
		if r.Code != 409 || !strings.Contains(r.Body.String(), "safe_apply_pending") {
			t.Fatal("partial recovery bypassed", r.Code)
		}
	}
	r := httptest.NewRecorder()
	handler.ServeHTTP(r, httptest.NewRequest("GET", "/v1/ready", nil))
	if r.Code != 503 || !strings.Contains(r.Body.String(), "recovery_required") || strings.Contains(r.Body.String(), "reboot_required") {
		t.Fatal("partial recovery reported reboot-ready")
	}
	if tx := safeTransaction(fake.transaction); !tx.RecoveryRequired || tx.RebootRequired {
		t.Fatal("misleading recovery projection")
	}
}
