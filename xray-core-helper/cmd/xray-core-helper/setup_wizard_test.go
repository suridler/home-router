package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/setup"
	"home-router/xray-core-helper/internal/unixhttp"
	"home-router/xray-core-helper/internal/xrayconfig"
)

func TestWizardDefaultsNeverApplyAndExistingAdoptionIsExplicit(t *testing.T) {
	for _, test := range []struct {
		answers string
		adopt   bool
	}{{"\n", false}, {"\n2\n\n", false}, {"bad\n\n2\n1\n", true}} {
		t.Run(test.answers, func(t *testing.T) {
			adoptions := 0
			server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/setup":
					_ = json.NewEncoder(w).Encode(map[string]any{"setupRequired": true, "configState": "existing", "configFingerprint": "fingerprint"})
				case "/v1/setup/adopt":
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["configFingerprint"] != "fingerprint" {
						t.Error("fingerprint lost")
					}
					adoptions++
					_ = json.NewEncoder(w).Encode(setup.Result{Status: "configured_direct"})
				default:
					t.Error("unexpected mutation or network selection", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			var out, stderr strings.Builder
			code := runSetup([]string{"setup"}, writeCLIConfig(t, server.socket), strings.NewReader(test.answers), &out, &stderr)
			if code != 0 || (adoptions == 1) != test.adopt {
				t.Fatal(code, adoptions, stderr.String())
			}
		})
	}
}

func TestWizardActivationDoesNotConfirmOrEnableOnDefault(t *testing.T) {
	posts := 0
	probes := 0
	server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/network/probe" {
			probes++
			json.NewEncoder(w).Encode(map[string]any{"reachable": false, "https": map[string]string{"status": "failed"}})
			return
		}
		if r.Method == http.MethodPost {
			posts++
		}
		if r.URL.Path == "/v1/templates/tproxy-unit/backups" {
			json.NewEncoder(w).Encode(map[string]any{"generation": 2})
			return
		}
		if r.URL.Path == "/v1/templates/tproxy-unit/apply" {
			json.NewEncoder(w).Encode(map[string]any{"transactionId": "fixture", "deadline": "2026-10-17T12:00:00Z"})
			return
		}
		t.Error("unexpected call", r.URL.Path)
		w.WriteHeader(500)
	}))
	var out, stderr strings.Builder
	client := unixhttp.NewClient(server.socket, time.Second)
	code := offerNetworkActivation(client, bufio.NewScanner(strings.NewReader("1\n\n")), &out, &stderr)
	if code != 0 || posts != 1 || probes != 2 {
		t.Fatal(code, posts, stderr.String())
	}
	// Default at the activation question must perform no request.
	posts = 0
	probes = 0
	if code = offerNetworkActivation(client, bufio.NewScanner(strings.NewReader("\n")), &out, &stderr); code != 0 || posts != 0 || probes != 0 {
		t.Fatal(code, posts)
	}
}

func TestWizardAllowsConfirmAfterFailedProbeAndRetryDoesNotReapply(t *testing.T) {
	var paths []string
	server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v1/templates/tproxy-unit/backups":
			json.NewEncoder(w).Encode(map[string]any{"generation": 2})
		case "/v1/templates/tproxy-unit/apply":
			json.NewEncoder(w).Encode(map[string]any{"transactionId": "tx-failed-probe", "deadline": "2099-10-17T12:00:00Z"})
		case "/v1/network/probe":
			var request struct{ Stage, TransactionID string }
			json.NewDecoder(r.Body).Decode(&request)
			if request.Stage == "after" && request.TransactionID != "tx-failed-probe" {
				t.Error("probe lost transaction")
			}
			json.NewEncoder(w).Encode(map[string]any{"reachable": false, "https": map[string]string{"status": "failed", "code": "tls_failed"}})
		case "/v1/safe-apply/tx-failed-probe/confirm", "/v1/xray/services/tproxy/enable":
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Error("unexpected call", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	var out, stderr strings.Builder
	code := offerNetworkActivation(unixhttp.NewClient(server.socket, time.Second), bufio.NewScanner(strings.NewReader("1\n3\n1\n")), &out, &stderr)
	counts := map[string]int{}
	for _, path := range paths {
		counts[path]++
	}
	if code != 0 || counts["/v1/templates/tproxy-unit/apply"] != 1 || counts["/v1/network/probe"] != 3 || counts["/v1/safe-apply/tx-failed-probe/confirm"] != 1 || counts["/v1/xray/services/tproxy/enable"] != 1 {
		t.Fatal(code, counts, stderr.String())
	}
}

func TestWizardDefaultsSelectOnlyInterfaceButNeverApply(t *testing.T) {
	plan := setup.Plan{Interface: "eth0", ListenAddress: "198.51.100.128", LANNetwork: "198.51.100.0/24", Gateway: &xrayconfig.Gateway{Port: 52345, Changes: []string{"outbounds.proxy"}}}
	for _, answers := range []string{"\n\n\n\n\n", "\n\n\n\n4\n\n"} {
		t.Run(answers, func(t *testing.T) {
			plans := 0
			server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/setup":
					json.NewEncoder(w).Encode(map[string]any{"setupRequired": true, "configState": "existing"})
				case "/v1/setup/interfaces":
					json.NewEncoder(w).Encode(api.SetupInterfacesResponse{Interfaces: []api.SetupInterface{{Name: "eth0", Addresses: []string{"198.51.100.128/24"}}}})
				case "/v1/setup/plan":
					plans++
					var request api.SetupPlanRequest
					json.NewDecoder(r.Body).Decode(&request)
					if request.Interface != plan.Interface || request.Address != plan.ListenAddress {
						t.Error("wrong default interface/address", request)
					}
					json.NewEncoder(w).Encode(map[string]any{"plan": plan})
				default:
					t.Error("unexpected mutation", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			var out, stderr strings.Builder
			code := runSetupWizard(unixhttp.NewClient(server.socket, time.Second), strings.NewReader(answers), &out, &stderr)
			if code != 0 || plans != 1 || !strings.Contains(out.String(), "адрес 198.51.100.128, подсеть 198.51.100.0/24") {
				t.Fatal(code, plans, out.String(), stderr.String())
			}
			if strings.Contains(out.String(), "outbounds.proxy") != strings.Contains(answers, "4") {
				t.Fatal("technical details must require explicit choice", out.String())
			}
		})
	}
}

func TestWizardMultipleInterfacesRequireExplicitChoice(t *testing.T) {
	server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/setup":
			json.NewEncoder(w).Encode(map[string]any{"setupRequired": true, "configState": "existing"})
		case "/v1/setup/interfaces":
			json.NewEncoder(w).Encode(api.SetupInterfacesResponse{Interfaces: []api.SetupInterface{
				{Name: "eth0", Addresses: []string{"192.0.2.1/24"}},
				{Name: "eth1", Addresses: []string{"198.51.100.1/24"}},
			}})
		default:
			t.Error("must not pick an interface or apply by default", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	var out, stderr strings.Builder
	code := runSetupWizard(unixhttp.NewClient(server.socket, time.Second), strings.NewReader("\n\n\n"), &out, &stderr)
	if code != 0 || !strings.Contains(out.String(), "Enter — 3") {
		t.Fatal(code, out.String(), stderr.String())
	}
}

func TestNetworkProbeDisplayUsesPlainLanguage(t *testing.T) {
	for _, success := range []bool{true, false} {
		result := api.NetworkProbeResult{
			Reachable: success, URL: "https://example.com/check", ExpectedStatus: 204,
			Gateway: "192.0.2.1", CheckedAt: time.Now(),
			DNS: api.NetworkProbeCheck{Status: "ok"}, GatewayPing: api.NetworkProbeCheck{Status: "failed"},
			HTTPS: api.NetworkProbeCheck{Code: "tls_failed"},
		}
		if success {
			result.HTTPStatus = 204
			result.HTTPS = api.NetworkProbeCheck{Status: "ok", Code: "https_ok"}
		}
		var out strings.Builder
		printNetworkProbeResult(&out, result)
		text := out.String()
		if strings.Contains(text, "https_ok") || strings.Contains(text, "tls_failed") || !strings.Contains(text, "не ответил на ping") {
			t.Fatal(text)
		}
		if strings.Contains(text, "Доступ в интернет работает.") != success {
			t.Fatal("gateway ping must not override HTTPS result", text)
		}
	}
}

func TestWizardExplicitRollbackNeverEnablesBoot(t *testing.T) {
	var mutations []string
	server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mutations = append(mutations, r.URL.Path)
		}
		switch r.URL.Path {
		case "/v1/templates/tproxy-unit/backups":
			json.NewEncoder(w).Encode(map[string]any{"generation": 2})
		case "/v1/templates/tproxy-unit/apply":
			json.NewEncoder(w).Encode(map[string]any{"transactionId": "tx", "deadline": "2099-10-17T12:00:00Z"})
		case "/v1/network/probe":
			json.NewEncoder(w).Encode(api.NetworkProbeResult{})
		case "/v1/safe-apply/tx/rollback":
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Error("unexpected call", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	var out, stderr strings.Builder
	code := offerNetworkActivation(unixhttp.NewClient(server.socket, time.Second), bufio.NewScanner(strings.NewReader("1\n2\n")), &out, &stderr)
	if code != 0 || len(mutations) != 4 || mutations[3] != "/v1/safe-apply/tx/rollback" {
		t.Fatal(code, mutations, stderr.String())
	}
}
