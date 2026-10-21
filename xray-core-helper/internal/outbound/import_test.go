package outbound

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestImportSupportedURIs(t *testing.T) {
	for _, tc := range []struct{ uri, protocol string }{
		{"vless://22222222-2222-4222-8222-222222222222@edge.example:443?security=tls&type=ws&path=%2Ftest&ignored=value#edge", "vless"},
		{"trojan://test-password@edge.example:443?security=tls&type=grpc&serviceName=test", "trojan"},
		{"ss://YWVzLTEyOC1nY206dGVzdC1wYXNzd29yZA@edge.example:443#edge", "shadowsocks"},
		{"socks://dXNlcjpwYXNz@edge.example:1080#edge", "socks"},
		{"vmess://22222222-2222-4222-8222-222222222222@edge.example:443?security=tls&type=tcp", "vmess"},
		{"hysteria2://test-password@edge.example:443?sni=edge.example", "hysteria"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			raw, err := Import(tc.uri)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if json.Unmarshal(raw, &value) != nil || value["protocol"] != tc.protocol {
				t.Fatal("wrong native projection")
			}
		})
	}
}
func TestImportRejectsUnsupportedAndMultipleInputsWithoutSecrets(t *testing.T) {
	for _, uri := range []string{"", "https://private-password.example", "vless://private-password", "{\"outbounds\":[]}", "dmxlc3M6Ly9wcml2YXRlLXBhc3N3b3Jk", "vless://private-password\ntrojan://private-password", strings.Repeat("x", 65<<10)} {
		_, err := Import(uri)
		if err == nil || strings.Contains(err.Error(), "private-password") {
			t.Fatal("unsupported input accepted or error leaked input")
		}
	}
}
