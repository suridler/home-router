package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/unixhttp"
)

func TestApplyTemplateInteractive(t *testing.T) {
	for _, test := range []struct {
		name, input, action                                         string
		code, probes                                                int
		expired, initialize, probeFails, confirmFails, stateChanged bool
	}{
		{name: "confirm", input: "1\n", action: "confirm", probes: 1},
		{name: "rollback", input: "2\n", action: "rollback", probes: 1},
		{name: "retry probe then confirm", input: "3\n1\n", action: "confirm", probes: 2},
		{name: "invalid answer then confirm", input: "oops\n1\n", action: "confirm", probes: 1},
		{name: "default leaves timer", input: "\n", code: 1, probes: 1},
		{name: "explicit leave", input: "4\n", code: 1, probes: 1},
		{name: "EOF", code: 1, probes: 1},
		{name: "expired", input: "1\n", code: 1, expired: true},
		{name: "initialization", input: "1\n", action: "confirm", initialize: true, probes: 1},
		{name: "failed probe can confirm", input: "1\n", action: "confirm", probeFails: true, probes: 1},
		{name: "failed confirmation", input: "1\n", action: "confirm", confirmFails: true, code: 1, probes: 1},
		{name: "state changed before confirm", input: "1\n", stateChanged: true, code: 1, probes: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			applyCalls, probes, heartbeats, resolves := 0, 0, 0, 0
			deadline := time.Now().Add(time.Minute)
			if test.expired {
				deadline = time.Now().Add(-time.Second)
			}
			revision := strings.Repeat("a", 64)
			server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/templates/nftables/apply":
					applyCalls++
					body, _ := io.ReadAll(r.Body)
					want := `{"expectedGeneration":7}`
					if test.initialize {
						want = `{"expectedGeneration":7,"initializeBlock":true,"expectedRevision":"` + revision + `"}`
					}
					if r.Method != http.MethodPost || string(body) != want {
						t.Errorf("apply request: %s %s", r.Method, body)
					}
					json.NewEncoder(w).Encode(api.ApplyTemplateResponse{TemplateID: "nftables", Applied: true, TransactionID: "tx-1", Deadline: deadline, Phase: "applied", State: "pending"})
				case "/v1/network/probe":
					probes++
					var request api.NetworkProbeRequest
					if json.NewDecoder(r.Body).Decode(&request) != nil || request.TransactionID != "tx-1" || request.Stage != "after" {
						t.Error("incorrect probe transaction")
					}
					if test.probeFails {
						w.WriteHeader(503)
						io.WriteString(w, `{"error":{"code":"probe_failed","message":"failed"}}`)
						return
					}
					io.WriteString(w, `{"reachable":false,"https":{"status":"failed","code":"https_failed"}}`)
				case "/v1/safe-apply":
					heartbeats++
					phase := "applied"
					if test.stateChanged && heartbeats > 1 {
						phase = "awaiting_reboot"
					}
					json.NewEncoder(w).Encode(api.SafeApplyResponse{Pending: &api.SafeApplyTransaction{TransactionID: "tx-1", Deadline: deadline, Phase: phase, State: "pending"}})
				case "/v1/safe-apply/tx-1/confirm", "/v1/safe-apply/tx-1/rollback":
					resolves++
					if !strings.HasSuffix(r.URL.Path, "/"+test.action) {
						t.Error("incorrect resolution", r.URL.Path)
					}
					if test.confirmFails {
						w.WriteHeader(409)
						io.WriteString(w, `{"error":{"code":"reboot_required","message":"failed"}}`)
						return
					}
					state := "confirmed"
					if test.action == "rollback" {
						state = "pending"
					}
					json.NewEncoder(w).Encode(api.SafeApplyResolutionResponse{Transaction: api.SafeApplyTransaction{TransactionID: "tx-1", State: state, RebootRequired: test.action == "rollback"}, Changed: true})
				default:
					t.Error("unexpected request", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			args := []string{"templates", "apply", "nftables", "--expected-generation", "7"}
			if test.initialize {
				args = append(args, "--initialize-block", "--expected-revision", revision)
			}
			var out, stderr strings.Builder
			code := runApplyTemplate(args, writeCLIConfig(t, server.socket), strings.NewReader(test.input), &out, &stderr)
			if code != test.code || applyCalls != 1 || probes != test.probes || (resolves == 1) != (test.action != "") {
				t.Fatalf("code=%d apply=%d probe=%d resolve=%d stderr=%s", code, applyCalls, probes, resolves, stderr.String())
			}
			if !test.expired && (!strings.Contains(stderr.String(), "Осталось") || !strings.Contains(stderr.String(), "ответ helper")) {
				t.Fatal("no live countdown", stderr.String())
			}
			if test.code != 0 && strings.Contains(stderr.String(), "Изменения подтверждены и сохранены") {
				t.Fatal("false success")
			}
			if test.initialize && !strings.Contains(stderr.String(), "INITIALIZE") {
				t.Fatal("missing initialization warning")
			}
		})
	}
}

func TestApplyWithoutTransactionDoesNotPrompt(t *testing.T) {
	server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/templates/xray-config/apply" {
			t.Error("unexpected request", r.URL.Path)
		}
		io.WriteString(w, `{"templateId":"xray-config","applied":true}`)
	}))
	var out, stderr strings.Builder
	if code := runApplyTemplate([]string{"templates", "apply", "xray-config", "--expected-generation", "0"}, writeCLIConfig(t, server.socket), strings.NewReader(""), &out, &stderr); code != 0 || strings.Contains(stderr.String(), "Осталось") {
		t.Fatal(code, stderr.String())
	}
}

func TestApplyCountdownStopsWithoutInputAtDeadlineOrConnectionLoss(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "connection lost"}[lost], func(t *testing.T) {
			checks, resolves := 0, 0
			deadline := time.Now().Add(150 * time.Millisecond)
			if lost {
				deadline = time.Now().Add(time.Minute)
			}
			server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/network/probe":
					io.WriteString(w, `{}`)
				case "/v1/safe-apply":
					checks++
					if lost && checks > 1 {
						w.WriteHeader(503)
						return
					}
					json.NewEncoder(w).Encode(api.SafeApplyResponse{Pending: &api.SafeApplyTransaction{TransactionID: "idle", Phase: "applied", State: "pending"}})
				default:
					resolves++
					t.Error("unexpected resolution", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			input, writer := io.Pipe()
			defer input.Close()
			defer writer.Close()
			var out, stderr strings.Builder
			code := confirmTemplateApply(unixhttp.NewClient(server.socket, time.Second), bufio.NewScanner(input), &out, &stderr, api.ApplyTemplateResponse{TransactionID: "idle", Deadline: deadline})
			if code != 1 || resolves != 0 {
				t.Fatal(code, resolves, stderr.String())
			}
			if lost && (checks < 2 || !strings.Contains(stderr.String(), "Связь с helper потеряна")) {
				t.Fatal(checks, stderr.String())
			}
			if !lost && !strings.Contains(stderr.String(), "Срок подтверждения истёк") {
				t.Fatal(stderr.String())
			}
		})
	}
}

func TestForceApplySendsFlagAndNeverPrompts(t *testing.T) {
	for _, flag := range []string{"-force", "--force"} {
		t.Run(flag, func(t *testing.T) {
			calls := 0
			server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				raw, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/v1/templates/tproxy-unit/apply" || string(raw) != `{"expectedGeneration":7,"force":true}` {
					t.Error(r.URL.Path, string(raw))
				}
				io.WriteString(w, `{"transactionId":"force-tx","state":"confirmed","applied":true}`)
			}))
			var out, stderr strings.Builder
			code := runApplyTemplate([]string{"templates", "apply", "tproxy-unit", "--expected-generation", "7", flag}, writeCLIConfig(t, server.socket), strings.NewReader(""), &out, &stderr)
			if code != 0 || calls != 1 || strings.Contains(stderr.String(), "Осталось") {
				t.Fatal(code, calls, stderr.String())
			}
		})
	}
}
