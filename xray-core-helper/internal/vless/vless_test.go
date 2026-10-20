package vless

import (
	"encoding/json"
	"strings"
	"testing"
)

const realityURI = "vless://11111111-1111-4111-8111-111111111111@edge.example:8443" +
	"?type=xhttp&security=reality&flow=xtls-rprx-vision&pbk=public-key-value&fp=firefox" +
	"&sni=front.example&sid=abcd1234&spx=%2Fx&path=%2Fp&mode=auto&host=h.example&xPaddingBytes=100-200&sc=fast"

func TestParseRealityXHTTP(t *testing.T) {
	parsed, err := Parse(realityURI, TransportKeyNetwork)
	if err != nil {
		t.Fatal(err)
	}
	want := Preview{
		Address: "edge.example", Port: 8443, Transport: "xhttp", Security: "reality",
		Flow: "xtls-rprx-vision", ServerName: "front.example", UnsupportedParameters: []string{},
	}
	if parsed.Preview.Address != want.Address || parsed.Preview.Port != want.Port ||
		parsed.Preview.Transport != want.Transport || parsed.Preview.Security != want.Security ||
		parsed.Preview.Flow != want.Flow || parsed.Preview.ServerName != want.ServerName ||
		len(parsed.Preview.UnsupportedParameters) != 0 {
		t.Fatalf("preview = %#v", parsed.Preview)
	}
	stream := parsed.StreamSettings
	if stream["network"] != "xhttp" || stream["security"] != "reality" {
		t.Fatalf("stream = %#v", stream)
	}
	reality := stream["realitySettings"].(map[string]any)
	if reality["publicKey"] != "public-key-value" || reality["shortId"] != "abcd1234" ||
		reality["fingerprint"] != "firefox" || reality["spiderX"] != "/x" {
		t.Fatalf("reality = %#v", reality)
	}
	xhttp := stream["xhttpSettings"].(map[string]any)
	extra := xhttp["extra"].(map[string]any)
	if xhttp["path"] != "/p" || xhttp["mode"] != "auto" || xhttp["host"] != "h.example" ||
		extra["xPaddingBytes"] != "100-200" || extra["speedControl"] != "fast" {
		t.Fatalf("xhttp = %#v", xhttp)
	}
	user := parsed.Settings["vnext"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
	if user["id"] != "11111111-1111-4111-8111-111111111111" || user["encryption"] != "none" {
		t.Fatalf("user = %#v", user)
	}
}

func TestPreviewOmitsCredentials(t *testing.T) {
	parsed, err := Parse(realityURI, TransportKeyNetwork)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(parsed.Preview)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"11111111-1111", "public-key-value", "abcd1234", "vless://"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("preview leaked %q: %s", secret, encoded)
		}
	}
}

func TestParseDefaultsAndMethodTransportKey(t *testing.T) {
	parsed, err := Parse("vless://id@host.example", TransportKeyMethod)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Preview.Port != 443 || parsed.Preview.Transport != "tcp" || parsed.Preview.Security != "none" ||
		parsed.Preview.Flow != "not configured" || parsed.Preview.ServerName != "not configured" {
		t.Fatalf("preview = %#v", parsed.Preview)
	}
	if parsed.StreamSettings["method"] != "raw" {
		t.Fatalf("stream = %#v", parsed.StreamSettings)
	}
	ws, err := Parse("vless://id@host.example?type=ws&sni=cdn.example", TransportKeyMethod)
	if err != nil {
		t.Fatal(err)
	}
	if ws.StreamSettings["method"] != "websocket" {
		t.Fatalf("ws stream = %#v", ws.StreamSettings)
	}
	settings := ws.StreamSettings["wsSettings"].(map[string]any)
	if settings["path"] != "/" || settings["headers"].(map[string]any)["Host"] != "cdn.example" {
		t.Fatalf("wsSettings = %#v", settings)
	}
}

func TestParseTLSAndGRPC(t *testing.T) {
	parsed, err := Parse("vless://id@host.example:443?type=grpc&security=tls&sni=s.example&alpn=h2, http/1.1&serviceName=svc", TransportKeyNetwork)
	if err != nil {
		t.Fatal(err)
	}
	tls := parsed.StreamSettings["tlsSettings"].(map[string]any)
	alpn := tls["alpn"].([]string)
	if tls["serverName"] != "s.example" || tls["fingerprint"] != "chrome" || len(alpn) != 2 || alpn[1] != "http/1.1" {
		t.Fatalf("tls = %#v", tls)
	}
	if parsed.StreamSettings["grpcSettings"].(map[string]any)["serviceName"] != "svc" {
		t.Fatalf("stream = %#v", parsed.StreamSettings)
	}
}

func TestParseReportsUnsupportedParametersSorted(t *testing.T) {
	parsed, err := Parse("vless://id@host.example?zeta=1&type=tcp&alpha=2", TransportKeyNetwork)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(parsed.Preview.UnsupportedParameters, ",")
	if got != "alpha,zeta" {
		t.Fatalf("unsupported = %q", got)
	}
}

func TestParseRejectsInvalidURIs(t *testing.T) {
	tests := map[string]string{
		"empty":        "",
		"wrong scheme": "https://id@host.example",
		"no user":      "vless://host.example:443",
		"no host":      "vless://id@:443",
		"port zero":    "vless://id@host.example:0",
		"port range":   "vless://id@host.example:70000",
		"bad query":    "vless://id@host.example?a=%zz",
		"too long":     "vless://id@host.example?x=" + strings.Repeat("a", maxURILength),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(raw, TransportKeyNetwork); err == nil {
				t.Fatalf("Parse(%q) succeeded", raw)
			}
		})
	}
}

func TestParseErrorsDoNotEchoInput(t *testing.T) {
	_, err := Parse("vless://secret-id@host.example:99999", TransportKeyNetwork)
	if err == nil || strings.Contains(err.Error(), "secret-id") {
		t.Fatalf("error = %v", err)
	}
}
