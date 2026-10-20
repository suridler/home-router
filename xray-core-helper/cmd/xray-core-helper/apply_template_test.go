package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestApplyTemplateCommandSendsOnlyFixedIDAndGeneration(t *testing.T) {
	server := startUnixAPIServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if request.Method != http.MethodPost || request.URL.Path != "/v1/templates/nftables/apply" || string(body) != `{"expectedGeneration":7}` {
			t.Errorf("request=%s %s body=%s", request.Method, request.URL.Path, body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"transactionId":"tx-1","templateId":"nftables","deadline":"2026-10-14T12:00:00Z","phase":"applied","state":"pending"}`)
	}))
	configPath := writeCLIConfig(t, server.socket)
	var stdout, stderr strings.Builder
	code := run([]string{"-config", configPath, "templates", "apply", "nftables", "--expected-generation", "7"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stderr.String(), "MANUAL SERVER REBOOT") || !strings.Contains(stdout.String(), "tx-1") || !strings.Contains(stdout.String(), "pending") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	for _, secret := range []string{"privateKey", "/etc/nftables.conf", "system bytes"} {
		if strings.Contains(stderr.String(), secret) {
			t.Fatalf("unsafe stderr=%s", stderr.String())
		}
	}
}

func TestApplyInitializationForwardsReviewedRevision(t *testing.T) {
	revision := strings.Repeat("a", 64)
	server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"expectedGeneration":7,"initializeBlock":true,"expectedRevision":"`+revision+`"}` {
			t.Errorf("incorrect initialization request")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"transactionId":"tx-1"}`)
	}))
	var out, errout strings.Builder
	code := run([]string{"-config", writeCLIConfig(t, server.socket), "templates", "apply", "nftables", "--expected-generation", "7", "--initialize-block", "--expected-revision", revision}, &out, &errout)
	if code != 0 || !strings.Contains(errout.String(), "INITIALIZE") {
		t.Fatal("explicit initialization failed", code)
	}
}
