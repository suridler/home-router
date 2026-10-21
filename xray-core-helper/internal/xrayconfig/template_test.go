package xrayconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"home-router/xray-core-helper/internal/domain"
	"home-router/xray-core-helper/internal/testconfig"
	"os"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/nativebinding"
)

func templateTestValues() TemplateValues {
	config := testconfig.Defaults()
	return TemplateValues{
		Network: bootstrap.NetworkConfig{Interface: "br-test", Address: "192.0.2.1", IPv4Prefix: "192.0.2.0/24", InterceptMark: 1, PolicyTable: 100},
		Logging: config.Logging, API: config.Xray.API,
		UserGeosite: config.Geodata.UserGeosite, UserGeoip: config.Geodata.UserGeoip,
		Xray: []byte(`{"inbounds":[{"tag":"tproxy-in","port":61000}],"outbounds":[{"tag":"direct","streamSettings":{"sockopt":{"mark":511}}}]}`),
	}
}

func TestRenderShippedXrayTemplate(t *testing.T) {
	raw, err := os.ReadFile("../../templConf/xray/config.json")
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(raw)
	values := templateTestValues()
	rendered, err := RenderTemplate(raw, values)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, original) || bytes.Contains(rendered, []byte("${")) {
		t.Fatal("input mutated or unresolved placeholder")
	}
	parsed, err := parseSource(rendered)
	if err != nil {
		t.Fatal(err)
	}
	log := parsed.value["log"].(map[string]any)
	if log["access"] != "none" || log["error"] != "" || log["loglevel"] != "warning" || log["dnsLog"] != false {
		t.Fatal("wrong logging defaults")
	}
	if parsed.value["api"].(map[string]any)["listen"] != values.API {
		t.Fatal("wrong API binding")
	}
	for _, token := range []string{`"port": 61000`, `"mark": 511`, `"listen": "192.0.2.1"`, `"ext:geoip_user.dat:USERLIST"`, `"ext:geosite_user.dat:USERLIST"`, `"domain:api.ipify.org"`, `"168.144.133.96"`, "// ЛОГИРОВАНИЕ", `"domainStrategy": "AsIs"`} {
		if !bytes.Contains(rendered, []byte(token)) {
			t.Fatalf("lost binding or policy: %s", token)
		}
	}
	again, err := RenderTemplate(rendered, values)
	if err != nil || !bytes.Equal(rendered, again) {
		t.Fatal("render not idempotent", err)
	}
	doc, err := nativebinding.Parse(rendered)
	if err != nil || doc.Check(values.Resolve) != nil {
		t.Fatal("rendered bindings cannot be checked", err)
	}
	values.Network.Address = "192.0.2.2"
	if !errors.Is(doc.Check(values.Resolve), nativebinding.ErrMismatch) {
		t.Fatal("stored/system address drift not detected")
	}
}

func TestRenderXrayTemplateEscapesStringsAndPreservesUnknowns(t *testing.T) {
	raw := []byte("{\n// keep unknown field\n\"unmanaged\":{\"value\":\"${untouched}\"},\n// helper:v1 bind file type=log-destination source=helper.logging.error-path\n\"error\": ${path}\n}\n")
	values := templateTestValues()
	values.Logging.ErrorPath = `/tmp/a"b\c.log`
	got, err := RenderTemplate(raw, values)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseSource(got)
	if err != nil || parsed.value["error"] != values.Logging.ErrorPath {
		t.Fatal("string was not escaped correctly", err)
	}
	if !strings.Contains(string(got), "// keep unknown field\n\"unmanaged\":{\"value\":\"${untouched}\"},") {
		t.Fatal("unmarked content changed")
	}
}

func TestRenderXrayTemplateFailsClosed(t *testing.T) {
	for _, tc := range []struct{ kind, source, statement, value string }{
		{"port", "xray.tproxy-port", `"x": ${value}`, ""},
		{"log-destination", "helper.logging.error-path", `"x": ${value}`, "../error.log"},
		{"log-destination", "helper.logging.error-path", `"x": ${value}`, "/tmp/error\n.log"},
		{"log-level", "helper.logging.level", `"x": ${value}`, "verbose"},
		{"loopback-endpoint", "helper.xray-api", `"x": ${value}`, "0.0.0.0:10085"},
		{"loopback-endpoint", "helper.xray-api", `"x": ${value}`, "127.0.0.1:0"},
		{"ipv4-address", "helper.network.address", `"x": ${value}`, "::1"},
		{"geoip-reference", "helper.user-geoip", `"x": ${value}`, "../geoip_user.dat"},
		{"geoip-reference", "helper.user-geoip", `"x": "${value}"`, "geoip_user.dat"},
		{"uint32", "xray.egress-mark", `"x": 1 // ${value}`, ""},
		{"uint32", "xray.egress-mark", `"x": ${value}, "y": ${value}`, ""},
		{"uint32", "unknown.source", `"x": ${value}`, ""},
	} {
		t.Run(tc.kind+tc.statement+tc.value, func(t *testing.T) {
			values := templateTestValues()
			values.Logging.ErrorPath = tc.value
			values.Logging.Level = tc.value
			values.API = tc.value
			values.Network.Address = tc.value
			values.UserGeoip = tc.value
			if tc.kind == "port" {
				values.Xray = []byte(`{}`)
			}
			raw := []byte("{\n// helper:v1 bind value type=" + tc.kind + " source=" + tc.source + "\n" + tc.statement + "\n}")
			if _, err := RenderTemplate(raw, values); err == nil {
				t.Fatal("invalid template accepted")
			}
		})
	}
}

func TestRenderUnmarkedXrayDoesNotRequireBindings(t *testing.T) {
	raw := []byte(`{"unknown":"` + strings.Repeat("x", (1<<20)+1) + `"}`)
	got, err := RenderTemplate(raw, TemplateValues{})
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("unmarked Xray config unnecessarily required bindings", err)
	}
}

func TestShippedTemplateRoutingAndDNSPolicy(t *testing.T) {
	raw := gatewayDefault(t)
	parsed, err := parseSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	rules := configRules(parsed.value)
	index := map[string]int{}
	for i, item := range rules {
		tag := safeString(asMap(item)["ruleTag"])
		if tag != "" {
			if _, exists := index[tag]; exists {
				t.Fatal("duplicate rule tag")
			}
			index[tag] = i
		}
	}
	ordered := []string{"direct-torrent", "direct-local", "proxy-domains-permanent", "proxy-ip-permanent", "block", "direct-domain-ru-only", "proxy-domain-ru-blocked", "proxy-domain-ru-blocked-kino", "proxy-ip-ru-blocked", "direct-ip-ru"}
	previous := -1
	for _, tag := range ordered {
		i, ok := index[tag]
		if !ok || i <= previous {
			t.Fatal("policy precedence changed", tag)
		}
		previous = i
	}
	if asMap(parsed.value["routing"])["domainStrategy"] != "AsIs" {
		t.Fatal("routing unexpectedly resolves domains")
	}
	outbounds := asSlice(parsed.value["outbounds"])
	if safeString(asMap(outbounds[0])["tag"]) != "direct" {
		t.Fatal("default outbound changed")
	}
	for _, value := range outbounds {
		out := asMap(value)
		if safeString(out["tag"]) == "proxy" && safeString(out["protocol"]) != "freedom" {
			t.Fatal("initial proxy must use direct egress")
		}
	}
	dns := asMap(parsed.value["dns"])
	servers := asSlice(dns["servers"])
	cloudflare := asMap(servers[0])
	if cloudflare["address"] != "https://cloudflare-dns.com/dns-query" || cloudflare["tag"] != "dns-proxy" || cloudflare["skipFallback"] != true || cloudflare["finalQuery"] != true || dns["queryStrategy"] != "UseIPv4" || dns["disableFallback"] != false {
		t.Fatal("DNS selection/fallback policy changed")
	}
	expected := append([]string{}, stringValues(asMap(rules[index["proxy-domain-ru-blocked"]])["domain"])...)
	expected = append(expected, stringValues(asMap(rules[index["proxy-domains-permanent"]])["domain"])...)
	actual := stringValues(cloudflare["domains"])
	if len(expected) != len(actual) {
		t.Fatal("DNS and permanent proxy domain conditions differ")
	}
	for i, value := range expected {
		if actual[i] != value {
			t.Fatal("DNS and proxy conditions differ")
		}
	}
	if asMap(servers[1])["address"] != "https+local://common.dot.dns.yandex.net/dns-query" || servers[2] != "localhost" {
		t.Fatal("ordinary DNS policy changed")
	}
	if bytes.Contains(raw, []byte(`"webhook"`)) {
		t.Fatal("routing webhook returned")
	}
	// The runtime overlay must precede permanent rules and leave system rules first.
	overlay, err := domain.BuildResourceOverlay([]string{"example.test", "192.0.2.7"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := readerForContent(t, string(raw)).RoutingPayload(context.Background(), overlay)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Rules []map[string]any `json:"rules"`
	}
	if err = json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Rules) != len(rules)+2 || result.Rules[4]["ruleTag"] != domain.AdditionsRuleTag || result.Rules[5]["ruleTag"] != domain.IPv4AdditionsRuleTag || result.Rules[6]["ruleTag"] != "proxy-domains-permanent" || result.Rules[3]["ruleTag"] != "direct-local" {
		t.Fatal("runtime overlay changed system/permanent precedence")
	}
}
