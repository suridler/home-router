package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/routerdiagnostics"
	"home-router/xray-core-helper/internal/safeapply"
)

type networkProbeFake struct{ calls int }

func (f *networkProbeFake) Probe(_ context.Context, r api.NetworkProbeRequest) (api.NetworkProbeResult, error) {
	f.calls++
	return api.NetworkProbeResult{Stage: r.Stage, TransactionID: r.TransactionID}, nil
}

func TestConnectivityUsesNamedServiceChecksWithoutFixedIndexes(t *testing.T) {
	connectivity := routerdiagnostics.NewConnectivity(func(context.Context) error { return nil }, func(context.Context, string) (string, error) { return "", nil })
	connectivity.DNS = func(context.Context) error { return nil }
	mux := http.NewServeMux()
	registerConnectivity(mux, ServerServices{Connectivity: connectivity, Diagnostics: &diagnosticsFake{result: routerdiagnostics.Result{Checks: []routerdiagnostics.Check{
		{Name: routerdiagnostics.CheckTProxyService, Status: routerdiagnostics.StatusOK},
		{Name: routerdiagnostics.CheckXrayService, Status: routerdiagnostics.StatusOK},
	}}}})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest("POST", "/v1/xray/diagnostics/connectivity", strings.NewReader(`{}`)))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"scope":"local_service_state"`) {
		t.Fatal(response.Code, response.Body.String())
	}
}
func (*networkProbeFake) History() api.NetworkProbeHistory { return api.NetworkProbeHistory{} }

func TestProbeAllowsObservationDuringPendingButRejectsWrongTransaction(t *testing.T) {
	apply := &applyTemplateFake{transaction: transactionFixture()}
	apply.transaction.ID = "tx-1"
	apply.transaction.Deadline = time.Now().Add(time.Minute)
	apply.transaction.Phase = safeapply.PhaseApplied
	probe := &networkProbeFake{}
	mux := http.NewServeMux()
	registerNetworkProbe(mux, probe, apply)
	handler := rebootGate(mux, apply)
	for _, test := range []struct {
		body   string
		status int
	}{
		{`{"stage":"after","transactionId":"tx-1"}`, 200},
		{`{"stage":"manual"}`, 200},
		{`{"stage":"after","transactionId":"wrong"}`, 409},
		{`{"stage":"before"}`, 409},
		{`{"stage":"manual","url":"https://private.example"}`, 400},
		{`{"stage":"manual","transactionId":"tx-1"}`, 400},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", "/v1/network/probe", strings.NewReader(test.body)))
		if response.Code != test.status {
			t.Fatalf("%s: %d %s", test.body, response.Code, response.Body.String())
		}
	}
	if probe.calls != 2 {
		t.Fatal(probe.calls)
	}
	apply.transaction.Phase = safeapply.PhaseAwaitingReboot
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("POST", "/v1/network/probe", strings.NewReader(`{"stage":"manual"}`)))
	if response.Code != 200 {
		t.Fatal(response.Code)
	}
}
