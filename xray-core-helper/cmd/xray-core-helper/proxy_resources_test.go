package main

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
)

func TestProxyResourcesCLIUsesTemporaryUnixAPI(t *testing.T) {
	tests := []struct {
		name                         string
		args                         []string
		method, path, requestBody    string
		responseBody, expectedOutput string
	}{
		{
			name:           "add IPv4 uses canonical response",
			args:           []string{"proxy-domain", "add", " 192.0.2.1 "},
			method:         http.MethodPost,
			path:           "/v1/proxy-domains",
			requestBody:    `{"domain":" 192.0.2.1 "}`,
			responseBody:   `{"domain":"192.0.2.1","applied":true}`,
			expectedOutput: "Resource \"192.0.2.1\": applied.\n",
		},
		{
			name:           "delete IPv4",
			args:           []string{"proxy-domain", "delete", "198.51.100.7"},
			method:         http.MethodDelete,
			path:           "/v1/proxy-domains/198.51.100.7",
			responseBody:   `{"domain":"198.51.100.7","applied":true}`,
			expectedOutput: "Resource \"198.51.100.7\": applied.\n",
		},
		{
			name:           "mixed list keeps JSON lists",
			args:           []string{"proxy-domain", "list"},
			method:         http.MethodGet,
			path:           "/v1/proxy-domains",
			responseBody:   `{"desired":["192.0.2.1","new.example"],"baseline":["198.51.100.7","old.example"],"consolidationActive":true}`,
			expectedOutput: "{\n  \"desired\": [\n    \"192.0.2.1\",\n    \"new.example\"\n  ],\n  \"baseline\": [\n    \"198.51.100.7\",\n    \"old.example\"\n  ],\n  \"consolidationActive\": true\n}\n",
		},
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
			args := append([]string{"-config", configPath}, test.args...)
			var stdout, stderr strings.Builder
			if exitCode := run(args, &stdout, &stderr); exitCode != 0 {
				t.Fatalf("run() = %d; stderr = %q", exitCode, stderr.String())
			}
			if stdout.String() != test.expectedOutput || stderr.Len() != 0 {
				t.Fatalf("stdout = %q, stderr = %q; want %q and empty stderr",
					stdout.String(), stderr.String(), test.expectedOutput)
			}
		})
	}
}

func TestProxyResourcesCLIExplainsErrorsWithoutServerMessages(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		status      int
		code        string
		wantPhrases []string
		avoid       []string
	}{
		{
			name:   "add rejects IPv6 with static explanation",
			args:   []string{"proxy-domain", "add", "2001:db8::1"},
			status: http.StatusBadRequest,
			code:   "invalid_domain",
			wantPhrases: []string{
				"proxy-domain: invalid_domain (HTTP 400)",
				"domain name or IPv4 address",
				"IPv6",
				"CIDR",
			},
			avoid: []string{"SECRET", "2001:db8::1"},
		},
		{
			name:   "delete rejects CIDR with static explanation",
			args:   []string{"proxy-domain", "delete", "192.0.2.0/24"},
			status: http.StatusBadRequest,
			code:   "invalid_domain",
			wantPhrases: []string{
				"proxy-domain: invalid_domain (HTTP 400)",
				"domain name or IPv4 address",
			},
			avoid: []string{"SECRET", "192.0.2.0/24"},
		},
		{
			name:   "add runtime failure may be persisted",
			args:   []string{"proxy-domain", "add", "192.0.2.1"},
			status: http.StatusServiceUnavailable,
			code:   "domain_runtime_sync_failed",
			wantPhrases: []string{
				"proxy-domain: domain_runtime_sync_failed (HTTP 503)",
				"may already be saved",
				"Re-read the list",
			},
			avoid: []string{"SECRET", "rollback", "reverted", "not saved"},
		},
		{
			name:   "delete runtime failure may be persisted",
			args:   []string{"proxy-domain", "delete", "198.51.100.7"},
			status: http.StatusServiceUnavailable,
			code:   "domain_runtime_sync_failed",
			wantPhrases: []string{
				"proxy-domain: domain_runtime_sync_failed (HTTP 503)",
				"may already be saved",
			},
			avoid: []string{"SECRET", "rollback", "reverted", "not saved"},
		},
		{
			name:   "apply runtime failure is unconfirmed",
			args:   []string{"proxy-domain", "apply"},
			status: http.StatusServiceUnavailable,
			code:   "domain_runtime_apply_failed",
			wantPhrases: []string{
				"proxy-domain: domain_runtime_apply_failed (HTTP 503)",
				"may already be saved",
				"Re-read the list",
			},
			avoid: []string{"SECRET", "rollback", "reverted", "not saved"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := startUnixAPIServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(test.status)
				_ = json.NewEncoder(writer).Encode(api.ErrorResponse{
					Error: api.ErrorDetail{Code: test.code, Message: "SECRET internal classification detail"},
				})
			}))
			configPath := writeCLIConfig(t, server.socket)
			args := append([]string{"-config", configPath}, test.args...)
			var stdout, stderr strings.Builder
			exitCode := run(args, &stdout, &stderr)
			if exitCode != 1 || stdout.Len() != 0 {
				t.Fatalf("run() = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
			}
			for _, phrase := range test.wantPhrases {
				if !strings.Contains(stderr.String(), phrase) {
					t.Errorf("stderr = %q; missing %q", stderr.String(), phrase)
				}
			}
			for _, phrase := range test.avoid {
				if strings.Contains(stderr.String(), phrase) {
					t.Errorf("stderr = %q; must not contain %q", stderr.String(), phrase)
				}
			}
		})
	}
}

func TestProxyResourcesCLIUsageAndHelp(t *testing.T) {
	var stdout, stderr strings.Builder
	if exitCode := run(nil, &stdout, &stderr); exitCode != 2 || stdout.Len() != 0 {
		t.Fatalf("run() = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
	}
	for _, line := range []string{
		"proxy-domain list",
		"proxy-domain add DOMAIN|IPV4",
		"proxy-domain delete DOMAIN|IPV4",
		"proxy-domain apply|reconcile",
	} {
		if !strings.Contains(stderr.String(), line) {
			t.Errorf("usage = %q; missing %q", stderr.String(), line)
		}
	}

	missingConfig := filepath.Join(t.TempDir(), "must-not-load.toml")
	commands := [][]string{
		{"proxy-domain"},
		{"proxy-domain", "add"},
		{"proxy-domain", "add", ""},
		{"proxy-domain", "add", "192.0.2.1", "extra"},
		{"proxy-domain", "delete"},
		{"proxy-domain", "delete", "old.example", "extra"},
		{"proxy-domain", "apply", "extra"},
		{"proxy-domain", "reconcile", "extra"},
		{"proxy-domain", "unknown"},
	}
	for _, command := range commands {
		t.Run(strings.Join(command, "_"), func(t *testing.T) {
			args := append([]string{"-config", missingConfig}, command...)
			var stdout, stderr strings.Builder
			if exitCode := run(args, &stdout, &stderr); exitCode != 2 {
				t.Fatalf("run() = %d, stdout = %q, stderr = %q; want usage failure before config load",
					exitCode, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage:") ||
				strings.Contains(stderr.String(), "no such file") {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
		})
	}
}
