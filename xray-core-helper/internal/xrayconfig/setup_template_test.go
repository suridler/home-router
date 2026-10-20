package xrayconfig

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestSetupTemplateDefaultsOnlyForAbsentProjections(t *testing.T) {
	raw, err := os.ReadFile("../../templConf/xray/config.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, current, port, mark string }{
		{"initial", "", "52345", "255"},
		{"partial", `{"outbounds":[{"tag":"proxy","protocol":"vless","streamSettings":{"sockopt":{"mark":511}}}]}`, "52345", "511"},
		{"port only", `{"inbounds":[{"tag":"tproxy-in","port":61000}]}`, "61000", "255"},
		{"both", `{"inbounds":[{"tag":"tproxy-in","port":61000}],"outbounds":[{"streamSettings":{"sockopt":{"mark":511}}}]}`, "61000", "511"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderSetupTemplate(raw, []byte(tc.current), templateTestValues())
			if err != nil {
				t.Fatal(err)
			}
			port, err := BindingValue(got, "xray.tproxy-port")
			if err != nil || port != tc.port {
				t.Fatal("wrong initial/existing port", err)
			}
			mark, err := BindingValue(got, "xray.egress-mark")
			if err != nil || mark != tc.mark {
				t.Fatal("wrong initial/existing mark", err)
			}
		})
	}
}

func TestSetupTemplateRejectsMalformedOrAmbiguousExistingProjections(t *testing.T) {
	raw := []byte("{\n// helper:v1 bind port type=port source=xray.tproxy-port\n\"port\": ${port}\n}\n")
	for _, current := range []string{
		`broken`, `{"inbounds":{}}`, `{"outbounds":null}`, `{"inbounds":[null]}`,
		`{"inbounds":[{"tag":"tproxy-in","port":0}]}`,
		`{"inbounds":[{"tag":"tproxy-in","port":"61000"}]}`,
		`{"inbounds":[{"tag":"tproxy-in"},{"tag":"tproxy-in"}]}`,
		`{"outbounds":[{"streamSettings":"invalid"}]}`,
		`{"outbounds":[{"streamSettings":null}]}`,
		`{"outbounds":[{"streamSettings":{"sockopt":[]}}]}`,
		`{"outbounds":[{"streamSettings":{"sockopt":null}}]}`,
		`{"outbounds":[{"streamSettings":{"sockopt":{"mark":0}}}]}`,
		`{"outbounds":[{"streamSettings":{"sockopt":{"mark":"511"}}}]}`,
		`{"outbounds":[{"streamSettings":{"sockopt":{"mark":255}}},{"streamSettings":{"sockopt":{"mark":511}}}]}`,
	} {
		if _, err := RenderSetupTemplate(raw, []byte(current), templateTestValues()); err == nil {
			t.Fatal("invalid projection silently defaulted")
		}
	}
}

func TestSetupTemplateKeepsExistingSecretsAndCommentsDuringPreparation(t *testing.T) {
	raw, err := os.ReadFile("../../templConf/xray/config.json")
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic credentials, never a host config.
	source := []byte(`{
 // keep operator comment
 "unknown":{"operator":true},
 "outbounds":[{"tag":"direct","protocol":"freedom","streamSettings":{"sockopt":{"mark":511}}},
 {"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"proxy.invalid","users":[{"id":"fixture-secret"}]}]},"streamSettings":{"network":"tcp","sockopt":{"mark":511}}}]
}`)
	defaults, err := RenderSetupTemplate(raw, source, templateTestValues())
	if err != nil {
		t.Fatal(err)
	}
	got, gateway, err := PrepareGateway(source, defaults)
	if err != nil || gateway.ProxyState != "configured" || gateway.EgressMark != 511 {
		t.Fatal("existing proxy not preserved", err)
	}
	for _, keep := range []string{"// keep operator comment", `"unknown":{"operator":true}`, `"protocol":"vless"`, `"id":"fixture-secret"`, `"network":"tcp"`} {
		if !bytes.Contains(got, []byte(keep)) {
			t.Fatal("operator config overwritten")
		}
	}
	if strings.Contains(string(got), "${") {
		t.Fatal("unresolved template inserted into config")
	}
}
