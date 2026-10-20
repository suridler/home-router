package main

import (
	"encoding/json"
	"home-router/xray-core-helper/internal/setup"
	"net/http"
	"strings"
	"testing"
)

func TestSetupNonInteractiveUsesPlanReturnedByDaemon(t *testing.T) {
	calls := []string{}
	server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/setup/plan":
			var request map[string]string
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request["interface"] != "lan0" {
				t.Errorf("request=%v", request)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"plan": setup.Plan{Interface: "lan0", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24", Artifacts: []setup.Artifact{{TemplateID: "xray-config", Action: "install", RuntimeAction: "start_xray"}}}})
		case "/v1/setup/apply":
			var plan setup.Plan
			_ = json.NewDecoder(r.Body).Decode(&plan)
			if plan.Interface != "lan0" || plan.ListenAddress != "192.0.2.1" {
				t.Errorf("plan=%#v", plan)
			}
			_ = json.NewEncoder(w).Encode(setup.Result{Status: "configured_direct", Interface: "lan0"})
		}
	}))
	config := writeCLIConfig(t, server.socket)
	var stdout, stderr strings.Builder
	code := runSetup([]string{"setup", "--interface", "lan0", "--non-interactive"}, config, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "configured_direct") || len(calls) != 2 {
		t.Fatalf("code=%d calls=%v stdout=%s stderr=%s", code, calls, stdout.String(), stderr.String())
	}
}
