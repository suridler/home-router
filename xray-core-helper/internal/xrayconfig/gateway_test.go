package xrayconfig

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func gatewayDefault(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../templConf/xray/config.json")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderSetupTemplate(raw, nil, templateTestValues())
	if err != nil {
		t.Fatal(err)
	}
	return rendered
}
func TestGatewayInitialProxyIsDirectWithoutDanglingRoutes(t *testing.T) {
	raw := gatewayDefault(t)
	g, err := InspectGateway(raw)
	if err != nil || g.ProxyState != "direct-placeholder" || g.Port != 52345 || g.EgressMark != 255 {
		t.Fatalf("projection=%+v error=%v", g, err)
	}
	prepared, g, err := PrepareGateway(raw, raw)
	if err != nil || !bytes.Equal(prepared, raw) || len(g.Changes) != 0 {
		t.Fatal("complete template changed", err)
	}
}
func TestGatewayPreservesConfiguredProxyUnknownFieldsAndComments(t *testing.T) {
	defaults := gatewayDefault(t)
	raw := bytes.Replace(defaults, []byte(`"tag": "proxy",`), []byte(`"tag": "proxy", "unknown": "kept",`), 1)
	// A real existing outbound must remain intact, including credentials and comments.
	raw = bytes.Replace(raw, []byte(`"tag": "proxy", "unknown": "kept",
      "protocol": "freedom",`), []byte(`"tag": "proxy", "unknown": "kept",
      "protocol": "vless", "settings": {"password":"fixture-only"},`), 1)
	got, g, err := PrepareGateway(raw, defaults)
	if err != nil || !bytes.Equal(got, raw) || g.ProxyState != "configured" {
		t.Fatal("existing config rewritten", err)
	}
}
func TestGatewayCompletesPartialWithoutReplacingUserRules(t *testing.T) {
	raw := []byte(`{ // keep this comment
 "unknown":{"keep":true},"inbounds":[{"tag":"tproxy-in","port":61000}],
 "outbounds":[{"tag":"direct","protocol":"freedom","streamSettings":{"sockopt":{"mark":42,"tcpFastOpen":true}}}],
 "routing":{"rules":[{"ruleTag":"custom","domain":["example.test"],"outboundTag":"direct"}]}}`)
	got, g, err := PrepareGateway(raw, gatewayDefault(t))
	if err != nil {
		t.Fatal(err)
	}
	if g.Port != 61000 || g.EgressMark != 42 || g.ProxyState != "direct-placeholder" || len(g.Changes) == 0 {
		t.Fatal(g)
	}
	for _, want := range []string{"// keep this comment", `"unknown":{"keep":true}`, `"ruleTag":"custom"`, `"tcpFastOpen":true`} {
		if !strings.Contains(string(got), want) {
			t.Fatal("lost existing content")
		}
	}
	again, _, err := PrepareGateway(got, gatewayDefault(t))
	if err != nil || !bytes.Equal(again, got) {
		t.Fatal("completion is not idempotent", err)
	}
}
func TestGatewayRejectsAmbiguityInsteadOfSilentlyReplacing(t *testing.T) {
	defaults := gatewayDefault(t)
	for _, raw := range [][]byte{
		[]byte(`not JSON`),
		[]byte(`{"inbounds":[{"tag":"tproxy-in","protocol":"socks"}]}`),
		[]byte(`{"inbounds":[{"tag":"tproxy-in","settings":{"network":"tcp"}}]}`),
		[]byte(`{"inbounds":[{"tag":"tproxy-in"},{"tag":"tproxy-in"}]}`),
		[]byte(`{"inbounds":[{"tag":"other","streamSettings":{"sockopt":{"tproxy":"tproxy"}}}]}`),
		[]byte(`{"outbounds":[{"tag":"a","streamSettings":{"sockopt":{"mark":42}}},{"tag":"b","streamSettings":{"sockopt":{"mark":43}}}]}`),
		[]byte(`{"inbounds":[{"tag":"tproxy-in","listen":"127.0.0.1"}]}`),
		[]byte(`{"routing":{"rules":[{"outboundTag":"missing"}]}}`),
	} {
		if _, _, err := PrepareGateway(raw, defaults); err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
	if _, _, err := PrepareGateway([]byte(`{}`), []byte(`{}`)); err == nil {
		t.Fatal("invalid factory accepted")
	}
}
