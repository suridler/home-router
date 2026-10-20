package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAcceptSystemCommandSendsConcurrencyTokens(t *testing.T) {
	server := startUnixAPIServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if request.Method != http.MethodPost || request.URL.Path != "/v1/templates/nftables/accept-system" || string(body) != `{"expectedGeneration":7,"expectedRevision":"revision-7"}` {
			t.Errorf("request=%s %s body=%s", request.Method, request.URL.Path, body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"templateId":"nftables","accepted":true,"backupId":"backup-8","generation":8,"revision":"revision-8","state":"synchronized"}`)
	}))
	configPath := writeCLIConfig(t, server.socket)
	var stdout, stderr strings.Builder
	code := run([]string{"-config", configPath, "templates", "accept-system", "nftables", "--expected-generation", "7", "--expected-revision", "revision-7"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"accepted": true`) {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	for _, secret := range []string{"privateKey", "config.json", "system bytes"} {
		if strings.Contains(stdout.String(), secret) {
			t.Fatalf("unsafe output=%s", stdout.String())
		}
	}
}
