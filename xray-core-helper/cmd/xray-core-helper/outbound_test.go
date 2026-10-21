package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOutboundCLIUsesSharedAPIAndStdin(t *testing.T) {
	for _, tc := range []struct{ command, input, method, path string }{
		{"list", "", "GET", "/v1/xray/outbounds"},
		{"get proxy", "", "GET", "/v1/xray/outbounds/proxy"},
		{"references extra", "", "GET", "/v1/xray/outbounds/extra/references"},
		{"import", "trojan://fixture-secret@edge.example:443", "POST", "/v1/xray/outbound-drafts/import"},
		{"create --expected-generation 7", `{"tag":"edge","protocol":"trojan","settings":{"password":"fixture-secret"}}`, "POST", "/v1/xray/outbound-drafts/apply"},
		{"update proxy --expected-generation 7 --restart", `{"protocol":"vless","settings":{"id":"fixture-secret"}}`, "PUT", "/v1/xray/outbounds/proxy"},
		{"replace proxy --expected-generation 7 --confirm-replacement", `{"protocol":"freedom"}`, "POST", "/v1/xray/outbounds/proxy/replace"},
		{"delete edge --expected-generation 7", "", "DELETE", "/v1/xray/outbounds/edge"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			called := false
			server := startUnixAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				if r.Method != "GET" {
					raw, _ := io.ReadAll(r.Body)
					var body map[string]any
					if json.Unmarshal(raw, &body) != nil {
						t.Error("invalid request JSON")
					}
					if tc.command != "import" && body["expectedGeneration"] != float64(7) {
						t.Error("generation lost")
					}
					if strings.HasPrefix(tc.command, "replace") && body["confirmReplacement"] != true {
						t.Error("replacement confirmation lost")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"generation":8}`))
			}))
			var out, diagnostic strings.Builder
			code := runOutbound(strings.Fields(tc.command), writeCLIConfig(t, server.socket), strings.NewReader(tc.input), &out, &diagnostic)
			if code != 0 || !called {
				t.Fatalf("code=%d diagnostic=%s", code, diagnostic.String())
			}
			if strings.Contains(diagnostic.String(), "fixture-secret") {
				t.Fatal("credentials leaked to diagnostics")
			}
		})
	}
}
func TestOutboundCLIRejectsAmbiguousWritesBeforeConnecting(t *testing.T) {
	for _, command := range []string{"create", "update proxy", "replace proxy --expected-generation 0", "create --expected-generation 0 --confirm-replacement", "list --restart", "delete"} {
		var out, diagnostic strings.Builder
		if runOutbound(strings.Fields(command), "/unavailable", strings.NewReader(`{"protocol":"freedom"}`), &out, &diagnostic) != 2 {
			t.Fatalf("did not reject %s", command)
		}
	}
}
