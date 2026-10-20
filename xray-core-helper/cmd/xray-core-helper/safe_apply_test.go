package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSafeApplyCLIStatusConfirmAndRollbackPending(t *testing.T) {
	calls := []string{}
	server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/safe-apply" {
			_, _ = io.WriteString(w, `{"pending":{"transactionId":"tx-1","templateId":"nftables","deadline":"2030-01-01T00:00:00Z","phase":"applied","state":"pending","generation":1}}`)
			return
		}
		_, _ = io.WriteString(w, `{"transaction":{"transactionId":"tx-1","templateId":"nftables","deadline":"2030-01-01T00:00:00Z","phase":"rolling_back","state":"rolled_back","generation":1},"changed":true}`)
	}))
	config := writeCLIConfig(t, server.socket)
	for _, args := range [][]string{{"-config", config, "safe-apply", "status"}, {"-config", config, "safe-apply", "confirm", "tx-1"}, {"-config", config, "safe-apply", "rollback-pending"}} {
		var stdout, stderr strings.Builder
		if code := run(args, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
	want := []string{"GET /v1/safe-apply", "POST /v1/safe-apply/tx-1/confirm", "GET /v1/safe-apply", "POST /v1/safe-apply/tx-1/rollback"}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls=%v", calls)
	}
}
