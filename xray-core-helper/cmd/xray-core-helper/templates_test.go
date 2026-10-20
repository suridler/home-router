package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTemplateCommandsPreserveOrderedSafeProjection(t *testing.T) {
	for _, test := range []struct{ command, method, path string }{
		{"status", http.MethodGet, "/v1/templates"},
		{"refresh", http.MethodPost, "/v1/templates/refresh"},
	} {
		t.Run(test.command, func(t *testing.T) {
			server := startUnixAPIServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != test.method || request.URL.Path != test.path {
					t.Errorf("request = %s %s", request.Method, request.URL.Path)
				}
				body, _ := io.ReadAll(request.Body)
				if len(body) != 0 {
					t.Errorf("body = %q", body)
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, `{"templates":[{"templateId":"xray-config","state":"different","revision":"one","summary":{"templateAvailable":true,"systemAvailable":true}},{"templateId":"nftables","state":"synchronized","revision":"two","summary":{"templateAvailable":true,"systemAvailable":true}}]}`)
			}))
			configPath := writeCLIConfig(t, server.socket)
			var stdout, stderr strings.Builder
			if code := run([]string{"-config", configPath, "templates", test.command}, &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			output := stdout.String()
			if strings.Index(output, "xray-config") > strings.Index(output, "nftables") {
				t.Fatalf("order changed: %s", output)
			}
			if strings.Contains(output, "config.json") || strings.Contains(output, "privateKey") {
				t.Fatalf("unsafe output: %s", output)
			}
		})
	}

}
func TestTemplateBackupCommandsUseTypedUnixAPI(t *testing.T) {
	tests := []struct {
		args               []string
		method, path, body string
	}{
		{[]string{"templates", "backups", "xray-config"}, http.MethodGet, "/v1/templates/xray-config/backups", ""},
		{[]string{"templates", "restore", "xray-config", "backup-1", "--expected-generation", "7"}, http.MethodPost, "/v1/templates/xray-config/backups/backup-1/restore", `{"expectedGeneration":7}`},
	}
	for _, test := range tests {
		t.Run(test.args[1], func(t *testing.T) {
			server := startUnixAPIServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				if request.Method != test.method || request.URL.Path != test.path || string(body) != test.body {
					t.Errorf("request=%s %s body=%s", request.Method, request.URL.Path, body)
				}
				writer.Header().Set("Content-Type", "application/json")
				if test.method == http.MethodGet {
					_, _ = io.WriteString(writer, `{"templateId":"xray-config","generation":7,"backups":[]}`)
				} else {
					_, _ = io.WriteString(writer, `{"templateId":"xray-config","backupId":"backup-1","generation":8,"restored":true}`)
				}
			}))
			args := append([]string{"-config", writeCLIConfig(t, server.socket)}, test.args...)
			var stdout, stderr strings.Builder
			if code := run(args, &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if strings.Contains(stdout.String(), "privateKey") || strings.Contains(stdout.String(), "config.json") {
				t.Fatalf("unsafe output=%s", stdout.String())
			}
		})
	}
}
