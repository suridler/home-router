package httpapi

import (
	"context"
	"encoding/json"
	"home-router/xray-core-helper/internal/setup"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type setupFake struct{}

func (setupFake) Status() setup.ConfigProbe {
	return setup.ConfigProbe{State: setup.ConfigEmpty, SetupRequired: true}
}

type blockedSetupFake struct{ setupFake }

func (blockedSetupFake) Status() setup.ConfigProbe {
	return setup.ConfigProbe{State: setup.ConfigUnavailable, SetupRequired: true, RecoveryRequired: true}
}
func (blockedSetupFake) Plan(context.Context, string) (setup.Plan, error) {
	return setup.Plan{}, setup.ErrRecoveryRequired
}
func (blockedSetupFake) Apply(context.Context, setup.Plan) (setup.Result, error) {
	return setup.Result{}, setup.ErrRecoveryRequired
}
func (blockedSetupFake) Recover(context.Context) error { return setup.ErrRecoveryRequired }

func TestSetupRecoveryFailureExposesStatusAndKeepsNormalAPIsUnavailable(t *testing.T) {
	handler := NewSetupServer("test", blockedSetupFake{}).Handler
	for _, path := range []string{"/v1/health", "/v1/setup"} {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		if r.Code != 200 || !strings.Contains(r.Body.String(), `"status":"recovery_required"`) {
			t.Fatalf("%s: %d %s", path, r.Code, r.Body.String())
		}
		if path == "/v1/setup" {
			var response struct{ RecoveryRequired, MutationsBlocked bool }
			if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil || !response.RecoveryRequired || !response.MutationsBlocked {
				t.Fatalf("response=%#v err=%v", response, err)
			}
		}
	}
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/v1/setup/plan", `{"interface":"lan0"}`, 503},
		{"POST", "/v1/setup/apply", `{}`, 503},
		{"POST", "/v1/setup/recover", "", 503},
		{"POST", "/v1/xray/config/adopt", `{"expectedGeneration":0}`, 404},
		{"POST", "/v1/templates/nftables/apply", `{"expectedGeneration":0}`, 404},
		{"GET", "/v1/xray/status", "", 404},
	} {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)))
		if r.Code != test.status {
			t.Fatalf("%s: %d %s", test.path, r.Code, r.Body.String())
		}
		if test.status == 503 && !strings.Contains(r.Body.String(), `"code":"recovery_required"`) {
			t.Fatalf("unbounded recovery error: %s", r.Body.String())
		}
	}
}
func (setupFake) Interfaces(context.Context) ([]setup.Interface, error) {
	return []setup.Interface{{Name: "lan0", Addresses: []string{"192.0.2.1/24"}}}, nil
}
func (setupFake) Plan(context.Context, string) (setup.Plan, error) {
	return setup.Plan{Interface: "lan0"}, nil
}
func (setupFake) Apply(_ context.Context, plan setup.Plan) (setup.Result, error) {
	return setup.Result{Status: "configured_direct", Interface: plan.Interface}, nil
}
func (setupFake) Recover(context.Context) error { return nil }
func TestSetupServerExposesOnlyBoundedSetupSurface(t *testing.T) {
	h := NewSetupServer("test", setupFake{}).Handler
	for _, test := range []struct{ path, want string }{{"/v1/health", "setup_required"}, {"/v1/setup", "empty"}, {"/v1/setup/interfaces", "lan0"}, {"/v1/xray/status", "not_found"}, {"/v1/templates", "not_found"}} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, test.path, nil))
		if !strings.Contains(r.Body.String(), test.want) {
			t.Errorf("%s: %d %s", test.path, r.Code, r.Body.String())
		}
	}
}
