package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
)

func TestProxyDomainAndGeodataCommandsUseVersionedUnixAPI(t *testing.T) {
	tests := []struct {
		name, command, method, path, requestBody, responseBody, output string
	}{
		{
			name:         "list",
			command:      "proxy-domain list",
			method:       http.MethodGet,
			path:         "/v1/proxy-domains",
			responseBody: `{"desired":["new.example"],"baseline":["old.example"],"consolidationActive":true}`,
			output:       "{\n  \"desired\": [\n    \"new.example\"\n  ],\n  \"baseline\": [\n    \"old.example\"\n  ],\n  \"consolidationActive\": true\n}\n",
		},
		{
			name:         "add",
			command:      "proxy-domain add NEW.Example.",
			method:       http.MethodPost,
			path:         "/v1/proxy-domains",
			requestBody:  `{"domain":"NEW.Example."}`,
			responseBody: `{"domain":"new.example","applied":true}`,
			output:       `Resource "new.example": applied.` + "\n",
		},
		{
			name:         "delete",
			command:      "proxy-domain delete old.example",
			method:       http.MethodDelete,
			path:         "/v1/proxy-domains/old.example",
			responseBody: `{"domain":"old.example","applied":true}`,
			output:       `Resource "old.example": applied.` + "\n",
		},
		{
			name: "reconcile", command: "proxy-domain reconcile", method: http.MethodPost,
			path: "/v1/proxy-domains/reconcile", requestBody: `{}`, responseBody: `{"applied":false}`,
			output: "{\n  \"applied\": false\n}\n",
		},
		{
			name:         "apply",
			command:      "proxy-domain apply",
			method:       http.MethodPost,
			path:         "/v1/proxy-domains/apply",
			requestBody:  `{}`,
			responseBody: `{"applied":true}`,
			output:       "Proxy-domain runtime synchronized.\n",
		},
	}

	runGeodataCommands := func(t *testing.T) {
		tests := []struct {
			name, command, method, path, key, responseBody, output string
			generatedKey                                           bool
		}{
			{
				name: "settings", command: "geodata settings", method: http.MethodGet,
				path:         "/v1/xray/geodata/settings",
				responseBody: `{"generation":2,"settings":{"origins":["https://data.example"],"geosite":{"url":"https://data.example/site.dat","checksum":{"url":"https://data.example/site.sha256","algorithm":"sha256"}},"geoip":{"url":"https://data.example/ip.dat","checksum":{"url":"https://data.example/ip.sha256","algorithm":"sha256"}}}}`,
				output:       "{\n  \"generation\": 2,\n  \"settings\": {\n    \"origins\": [\n      \"https://data.example\"\n    ],\n    \"geosite\": {\n      \"url\": \"https://data.example/site.dat\",\n      \"checksum\": {\n        \"url\": \"https://data.example/site.sha256\",\n        \"algorithm\": \"sha256\"\n      }\n    },\n    \"geoip\": {\n      \"url\": \"https://data.example/ip.dat\",\n      \"checksum\": {\n        \"url\": \"https://data.example/ip.sha256\",\n        \"algorithm\": \"sha256\"\n      }\n    }\n  }\n}\n",
			},
			{
				name: "status", command: "geodata status", method: http.MethodGet,
				path:         "/v1/xray/geodata/update/status",
				responseBody: `{"settings":{"generation":0,"settings":{"origins":[],"geosite":{"url":"","checksum":{"url":"","algorithm":"sha256"}},"geoip":{"url":"","checksum":{"url":"","algorithm":"sha256"}}}},"recovery":{"phase":"clean","code":"none","mutationsBlocked":false}}`,
				output:       "{\n  \"settings\": {\n    \"generation\": 0,\n    \"settings\": {\n      \"origins\": [],\n      \"geosite\": {\n        \"url\": \"\",\n        \"checksum\": {\n          \"url\": \"\",\n          \"algorithm\": \"sha256\"\n        }\n      },\n      \"geoip\": {\n        \"url\": \"\",\n        \"checksum\": {\n          \"url\": \"\",\n          \"algorithm\": \"sha256\"\n        }\n      }\n    }\n  },\n  \"recovery\": {\n    \"phase\": \"clean\",\n    \"code\": \"none\",\n    \"mutationsBlocked\": false\n  }\n}\n",
			},
			{
				name: "update", command: "geodata update --idempotency-key cli-key",
				method: http.MethodPost, path: "/v1/xray/geodata/update", key: "cli-key",
				responseBody: `{"operation":{"id":"0123456789abcdef0123456789abcdef","settingsGeneration":1,"state":"succeeded","resultCode":"geodata_update_succeeded","createdAt":1,"updatedAt":2,"finishedAt":2}}`,
				output:       "{\n  \"operation\": {\n    \"id\": \"0123456789abcdef0123456789abcdef\",\n    \"settingsGeneration\": 1,\n    \"state\": \"succeeded\",\n    \"resultCode\": \"geodata_update_succeeded\",\n    \"createdAt\": 1,\n    \"updatedAt\": 2,\n    \"finishedAt\": 2\n  }\n}\n",
			},
			{
				name: "update generated key", command: "geodata update", generatedKey: true,
				method: http.MethodPost, path: "/v1/xray/geodata/update",
				responseBody: `{"operation":{"id":"0123456789abcdef0123456789abcdef","settingsGeneration":1,"state":"succeeded","resultCode":"geodata_update_succeeded","createdAt":1,"updatedAt":2,"finishedAt":2}}`,
				output:       "{\n  \"operation\": {\n    \"id\": \"0123456789abcdef0123456789abcdef\",\n    \"settingsGeneration\": 1,\n    \"state\": \"succeeded\",\n    \"resultCode\": \"geodata_update_succeeded\",\n    \"createdAt\": 1,\n    \"updatedAt\": 2,\n    \"finishedAt\": 2\n  }\n}\n",
			},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				server := startUnixAPIServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					body, err := io.ReadAll(request.Body)
					if err != nil {
						t.Errorf("read request body: %v", err)
					}
					if request.Method != test.method || request.URL.Path != test.path || len(body) != 0 {
						t.Errorf("request=%s %s body=%q", request.Method, request.URL.Path, body)
					}
					key := request.Header.Get("Idempotency-Key")
					if test.generatedKey {
						if !strings.HasPrefix(key, "cli-") || !api.ValidIdempotencyKey(key) {
							t.Errorf("generated Idempotency-Key=%q", key)
						}
					} else if key != test.key {
						t.Errorf("Idempotency-Key=%q want %q", key, test.key)
					}
					writer.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(writer, test.responseBody)
				}))
				configPath := writeCLIConfig(t, server.socket)
				args := []string{"-config", configPath}
				args = append(args, strings.Split(test.command, " ")...)
				var stdout, stderr strings.Builder
				if exitCode := run(args, &stdout, &stderr); exitCode != 0 {
					t.Fatalf("run()=%d stderr=%q", exitCode, stderr.String())
				}
				if stdout.String() != test.output || stderr.Len() != 0 {
					t.Fatalf("stdout=%q stderr=%q; want %q", stdout.String(), stderr.String(), test.output)
				}
			})
		}
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := startUnixAPIServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
				}
				if request.Method != test.method || request.URL.Path != test.path || string(body) != test.requestBody {
					t.Errorf("request = %s %s body %q, want %s %s body %q",
						request.Method, request.URL.Path, body, test.method, test.path, test.requestBody)
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, test.responseBody)
			}))
			configPath := writeCLIConfig(t, server.socket)
			var args []string
			args = append(args, "-config", configPath)
			args = append(args, strings.Split(test.command, " ")...)

			var stdout, stderr strings.Builder
			if exitCode := run(args, &stdout, &stderr); exitCode != 0 {
				t.Fatalf("run() = %d; stderr = %q", exitCode, stderr.String())
			}
			if stdout.String() != test.output || stderr.Len() != 0 {
				t.Fatalf("output = %q, stderr = %q; want %q and empty stderr",
					stdout.String(), stderr.String(), test.output)
			}
		})
	}
	runGeodataCommands(t)
}

func TestProxyDomainCLIReportsStructuredErrorsWithoutServerMessages(t *testing.T) {
	tests := []struct {
		name, code, explanation string
		status                  int
	}{
		{name: "consolidation conflict", code: "consolidation_active", status: http.StatusConflict},
		{
			name: "possibly persisted runtime failure", code: "domain_runtime_sync_failed", status: http.StatusServiceUnavailable,
			explanation: "the change may already be saved, but runtime synchronization failed. Re-read the list before retrying.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := startUnixAPIServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(test.status)
				_ = json.NewEncoder(writer).Encode(api.ErrorResponse{
					Error: api.ErrorDetail{Code: test.code, Message: "SECRET raw RoutingService output"},
				})
			}))
			configPath := writeCLIConfig(t, server.socket)
			var stdout, stderr strings.Builder
			exitCode := run([]string{"-config", configPath, "proxy-domain", "add", "locked.example"}, &stdout, &stderr)
			if exitCode != 1 || stdout.Len() != 0 {
				t.Fatalf("run() = %d, stdout = %q", exitCode, stdout.String())
			}
			want := fmt.Sprintf("proxy-domain: %s (HTTP %d)\n", test.code, test.status)
			if test.explanation != "" {
				want = fmt.Sprintf("proxy-domain: %s (HTTP %d): %s\n", test.code, test.status, test.explanation)
			}
			if stderr.String() != want || strings.Contains(stderr.String(), "SECRET") ||
				strings.Contains(stderr.String(), "RoutingService") {
				t.Fatalf("stderr = %q; want %q without server details", stderr.String(), want)
			}
		})
	}
}

func TestProxyDomainCLIReportsUnavailableSocket(t *testing.T) {
	configPath := writeCLIConfig(t, filepath.Join(t.TempDir(), "missing.sock"))
	var stdout, stderr strings.Builder
	exitCode := run([]string{"-config", configPath, "proxy-domain", "list"}, &stdout, &stderr)
	if exitCode != 1 || stdout.Len() != 0 || stderr.String() != "proxy-domain: helper daemon unavailable\n" {
		t.Fatalf("run() = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
	}
}

func TestOperationalCommandsUseStrictVersionedUnixAPI(t *testing.T) {
	tests := []struct {
		name, command, method, path, query, requestBody, responseBody, output string
	}{
		{
			name: "validate", command: "validate", method: http.MethodPost,
			path: "/v1/xray/config/validate", requestBody: `{}`,
			responseBody: `{"status":"valid"}`,
			output:       "{\n  \"status\": \"valid\"\n}\n",
		},
		{
			name: "diagnostics", command: "diagnostics", method: http.MethodGet,
			path:         "/v1/xray/diagnostics",
			responseBody: `{"checks":[{"name":"default_route","status":"ok"}]}`,
			output:       "{\n  \"checks\": [\n    {\n      \"name\": \"default_route\",\n      \"status\": \"ok\"\n    }\n  ]\n}\n",
		},
		{
			name: "service", command: "service tproxy restart", method: http.MethodPost,
			path: "/v1/xray/services/tproxy/restart", requestBody: `{}`,
			responseBody: `{"unit":"xray-tproxy-routing.service","action":"restart"}`,
			output:       "{\n  \"unit\": \"xray-tproxy-routing.service\",\n  \"action\": \"restart\"\n}\n",
		},
		{
			name: "logs default limit", command: "logs access", method: http.MethodGet,
			path: "/v1/logs/access", query: "limit=100",
			responseBody: `{"kind":"access","source":"none","lines":[],"bytesRead":0,"byteLimit":1048576,"lineLimit":100,"truncated":false}`,
			output:       "{\n  \"kind\": \"access\",\n  \"source\": \"none\",\n  \"lines\": [],\n  \"bytesRead\": 0,\n  \"byteLimit\": 1048576,\n  \"lineLimit\": 100,\n  \"truncated\": false\n}\n",
		},
		{
			name: "logs explicit limit", command: "logs error --limit 25", method: http.MethodGet,
			path: "/v1/logs/error", query: "limit=25",
			responseBody: `{"kind":"error","source":"journal","lines":["safe"],"bytesRead":4,"byteLimit":1048576,"lineLimit":25,"truncated":false}`,
			output:       "{\n  \"kind\": \"error\",\n  \"source\": \"journal\",\n  \"lines\": [\n    \"safe\"\n  ],\n  \"bytesRead\": 4,\n  \"byteLimit\": 1048576,\n  \"lineLimit\": 25,\n  \"truncated\": false\n}\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := startUnixAPIServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
				}
				if request.Method != test.method || request.URL.Path != test.path ||
					request.URL.RawQuery != test.query || string(body) != test.requestBody {
					t.Errorf("request=%s %s?%s body=%q; want %s %s?%s body=%q",
						request.Method, request.URL.Path, request.URL.RawQuery, body,
						test.method, test.path, test.query, test.requestBody)
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, test.responseBody)
			}))
			configPath := writeCLIConfig(t, server.socket)
			args := append([]string{"-config", configPath}, strings.Split(test.command, " ")...)
			var stdout, stderr strings.Builder
			if exitCode := run(args, &stdout, &stderr); exitCode != 0 {
				t.Fatalf("run()=%d stderr=%q", exitCode, stderr.String())
			}
			if stdout.String() != test.output || stderr.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q; want %q", stdout.String(), stderr.String(), test.output)
			}
		})
	}
}

func TestOperationalCLIRejectsArbitraryUnitsActionsPathsAndKinds(t *testing.T) {
	missingConfig := filepath.Join(t.TempDir(), "must-not-load.toml")
	commands := [][]string{
		{"service", "ssh", "restart"},
		{"service", "xray.service", "restart"},
		{"service", "xray", "status"},
		{"service", "xray", "restart", "extra"},
		{"logs", "audit"},
		{"logs", "/etc/passwd"},
		{"logs", "access", "--path", "/etc/passwd"},
		{"logs", "error", "--limit", "0"},
		{"logs", "error", "--limit", "201"},
		{"logs", "error", "--limit", "10", "extra"},
		{"validate", "/tmp/other-config"},
		{"diagnostics", "--command", "ip route"},
	}
	for _, command := range commands {
		t.Run(strings.Join(command, "_"), func(t *testing.T) {
			args := append([]string{"-config", missingConfig}, command...)
			var stdout, stderr strings.Builder
			if exitCode := run(args, &stdout, &stderr); exitCode != 2 {
				t.Fatalf("run()=%d stdout=%q stderr=%q; want usage failure before config load", exitCode, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage:") || strings.Contains(stderr.String(), "no such file") {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

type unixAPIServer struct {
	socket string
}

func startUnixAPIServer(t *testing.T, handler http.Handler) unixAPIServer {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "helper.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	return unixAPIServer{socket: socket}
}

func writeCLIConfig(t *testing.T, socket string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helper.toml")
	content := fmt.Sprintf("[server]\nsocket = %q\n", socket)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGeodataSizeErrorDisplaysSafeLimit(t *testing.T) {
	err := apiErrorFromResponse(503, []byte(`{"error":{"code":"geodata_asset_too_large","message":"SECRET","maxAssetSizeMB":512}}`))
	var stderr strings.Builder
	writeRequestError(&stderr, "geodata", err)
	if !strings.Contains(stderr.String(), "превышает заданный лимит — 512 МБ") || strings.Contains(stderr.String(), "SECRET") {
		t.Fatal(stderr.String())
	}
}
