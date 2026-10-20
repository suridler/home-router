package xrayconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"home-router/xray-core-helper/internal/domain"
	"reflect"
	"strings"
	"testing"
)

func managedUserGeositeIndex(t *testing.T, source []byte) int {
	t.Helper()
	parsed, err := parseSource(source)
	if err != nil {
		t.Fatalf("result no longer parses: %v\n%s", err, source)
	}
	index := -1
	for position, value := range configRules(parsed.value) {
		if safeString(asMap(value)["ruleTag"]) == userGeositeRuleTag {
			if index >= 0 {
				t.Fatalf("duplicate managed rule in result:\n%s", source)
			}
			index = position
		}
	}
	if index < 0 {
		t.Fatalf("managed rule missing:\n%s", source)
	}
	return index
}

func requireUserGeositeAnnotation(t *testing.T, source []byte, filename string) {
	t.Helper()
	annotation := userGeositeAnnotation(filename)
	start := bytes.Index(source, []byte(annotation))
	if start < 0 {
		t.Fatalf("annotation %q missing:\n%s", annotation, source)
	}
	reference := userGeositeReference(filename)
	scalar := bytes.Index(source[start+len(annotation):], []byte(`"`+reference+`"`))
	if scalar < 0 {
		t.Fatalf("domain scalar %q missing:\n%s", reference, source)
	}
	between := source[start+len(annotation) : start+len(annotation)+scalar]
	if strings.TrimSpace(string(between)) != "" {
		t.Fatalf("annotation is not immediately before the scalar: %q\n%s", between, source)
	}
}

func TestPrepareUserGeositeFactoryTemplatePlacement(t *testing.T) {
	raw := gatewayDefault(t)
	filename := "geosite_factory.dat"
	got, changed, err := PrepareUserGeosite(raw, filename)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("factory template was not changed")
	}
	parsed, err := parseSource(got)
	if err != nil {
		t.Fatalf("result no longer parses: %v\n%s", err, got)
	}
	rules := configRules(parsed.value)
	original, err := parseSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	originalRules := configRules(original.value)
	if len(rules) != len(originalRules) {
		t.Fatal("template rules lost during binding")
	}
	for i := range rules {
		if safeString(asMap(rules[i])["ruleTag"]) != safeString(asMap(originalRules[i])["ruleTag"]) {
			t.Fatal("template rule order changed")
		}
	}
	managed := managedUserGeositeIndex(t, got)
	if managed != 4 {
		t.Fatalf("managed rule index = %d; want 4", managed)
	}
	if safeString(asMap(rules[0])["outboundTag"]) != "dns-out" ||
		safeString(asMap(rules[1])["outboundTag"]) != "proxy" ||
		safeString(asMap(rules[2])["ruleTag"]) != "direct-torrent" ||
		safeString(asMap(rules[3])["ruleTag"]) != "direct-local" ||
		safeString(asMap(rules[5])["ruleTag"]) != "proxy-ip-permanent" {
		t.Fatal("protected rule order changed")
	}
	rule := asMap(rules[managed])
	if safeString(rule["type"]) != "field" ||
		safeString(rule["outboundTag"]) != "proxy" ||
		safeString(rule["ruleTag"]) != userGeositeRuleTag {
		t.Fatalf("managed rule = %#v", rule)
	}
	domains := stringValues(rule["domain"])
	if len(domains) != 1 || domains[0] != "ext:"+filename+":USERLIST" {
		t.Fatalf("managed domains = %v", domains)
	}
	if !bytes.Contains(got, []byte("// helper:v1 bind routing-user-geosite type=geosite-reference source=helper.user-geosite")) {
		t.Fatal("routing binding lost")
	}
	for _, keep := range []string{
		"// Не выполнять дополнительное DNS-разрешение для выбора маршрута.",
		`"direct-local"`,
		`"proxy-ip-permanent"`,
	} {
		if !strings.Contains(string(got), keep) {
			t.Fatalf("lost unrelated content %q:\n%s", keep, got)
		}
	}
	bound, err := BindUserGeosite(got, "geosite_factory_new.dat")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(bound, []byte("ext:geosite_factory_new.dat:USERLIST")) ||
		bytes.Contains(bound, []byte(filename)) {
		t.Fatalf("annotation is not bindable:\n%s", bound)
	}
}

func TestPrepareUserGeositePreservesUserRulesAndComments(t *testing.T) {
	raw := []byte(`{
  // operator note
  "api": {"tag": "api"},
  "outbounds": [
    {"tag": "proxy", "protocol": "freedom"},
    {"tag": "direct", "protocol": "freedom"},
    {"tag": "dns-out", "protocol": "dns"}
  ],
  "routing": {
    // routing note
    "rules": [
      {"type": "field", "inboundTag": ["api"], "outboundTag": "api"},
      {"type": "field", "inboundTag": ["dns-in"], "outboundTag": "dns-out"},
      {"type": "field", "ip": ["geoip:private"], "outboundTag": "direct", "ruleTag": "system-private-direct"},
      {
        // user note stays
        "type": "field",
        "domain": ["example.com"],
        "outboundTag": "direct",
        "ruleTag": "user-keep",
        "custom": {"keep": true}
      },
      {"type": "field", "inboundTag": ["tproxy-in"], "outboundTag": "proxy", "ruleTag": "system-tproxy-default"}
    ]
  }
}`)
	filename := "geosite_user.dat"
	got, changed, err := PrepareUserGeosite(raw, filename)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("missing managed rule was not added")
	}
	managed := managedUserGeositeIndex(t, got)
	if managed != 3 {
		t.Fatalf("managed rule index = %d; want 3\n%s", managed, got)
	}
	for _, keep := range []string{
		"// operator note",
		"// routing note",
		"// user note stays",
		`"custom": {"keep": true}`,
		`"ruleTag": "user-keep"`,
		`"ruleTag": "system-private-direct"`,
		`"ruleTag": "system-tproxy-default"`,
	} {
		if !strings.Contains(string(got), keep) {
			t.Fatalf("lost %q:\n%s", keep, got)
		}
	}
	parsed, err := parseSource(got)
	if err != nil {
		t.Fatal(err)
	}
	rules := configRules(parsed.value)
	if safeString(asMap(rules[4])["ruleTag"]) != "user-keep" ||
		safeString(asMap(rules[5])["ruleTag"]) != "system-tproxy-default" {
		t.Fatalf("user rules moved:\n%s", got)
	}
	requireUserGeositeAnnotation(t, got, filename)

	again, changedAgain, err := PrepareUserGeosite(got, filename)
	if err != nil {
		t.Fatal(err)
	}
	if changedAgain || !bytes.Equal(got, again) {
		t.Fatalf("idempotent preparation changed bytes (changed=%v)", changedAgain)
	}
}

func TestPrepareUserGeositeCustomFilenameBeforeFallback(t *testing.T) {
	raw := []byte(`{"routing":{"rules":[{"type":"field","inboundTag":["tproxy-in"],"outboundTag":"proxy","ruleTag":"system-tproxy-default"}]}}`)
	filename := "geosite_lab-2.dat"
	got, changed, err := PrepareUserGeosite(raw, filename)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || managedUserGeositeIndex(t, got) != 0 {
		t.Fatalf("managed rule was not inserted before the fallback:\n%s", got)
	}
	requireUserGeositeAnnotation(t, got, filename)
	if !strings.Contains(string(got), `"ext:`+filename+`:USERLIST"`) {
		t.Fatalf("custom filename missing:\n%s", got)
	}
}

func TestPrepareUserGeositeReusesExactReference(t *testing.T) {
	raw := []byte(`{
  "routing": {"rules": [
    {"type": "field", "inboundTag": ["api"], "outboundTag": "api"},
    {
      // reuse comment stays
      "type": "field",
      "domain": ["ext:geosite_reuse.dat:USERLIST"],
      "outboundTag": "proxy"
    },
    {"type": "field", "inboundTag": ["tproxy-in"], "outboundTag": "proxy", "ruleTag": "system-tproxy-default"}
  ]}
}`)
	filename := "geosite_reuse.dat"
	got, changed, err := PrepareUserGeosite(raw, filename)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("existing reference was not adopted")
	}
	parsed, err := parseSource(got)
	if err != nil {
		t.Fatal(err)
	}
	rules := configRules(parsed.value)
	if len(rules) != 3 {
		t.Fatalf("reference was duplicated, rules = %d:\n%s", len(rules), got)
	}
	if managedUserGeositeIndex(t, got) != 1 {
		t.Fatalf("managed rule was not reused in place:\n%s", got)
	}
	rule := asMap(rules[1])
	if safeString(rule["outboundTag"]) != "proxy" || safeString(rule["type"]) != "field" {
		t.Fatalf("reused rule = %#v", rule)
	}
	if !strings.Contains(string(got), "// reuse comment stays") {
		t.Fatalf("reuse comment lost:\n%s", got)
	}
	requireUserGeositeAnnotation(t, got, filename)
	if strings.Count(string(got), `"ext:`+filename+`:USERLIST"`) != 1 {
		t.Fatalf("reference count is ambiguous:\n%s", got)
	}
	bound, err := BindUserGeosite(got, "geosite_reuse_new.dat")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(bound, []byte("ext:geosite_reuse_new.dat:USERLIST")) {
		t.Fatalf("reused annotation is not bindable:\n%s", bound)
	}

	again, changedAgain, err := PrepareUserGeosite(got, filename)
	if err != nil {
		t.Fatal(err)
	}
	if changedAgain || !bytes.Equal(got, again) {
		t.Fatalf("reused rule was not idempotent (changed=%v)", changedAgain)
	}
}

func TestPrepareUserGeositeKeepsCorrectManagedRule(t *testing.T) {
	filename := "geosite_user.dat"
	raw := []byte(`{"routing":{"rules":[{"type":"field","domain":[
// helper:v1 bind user-geosite type=geosite-reference source=helper.user-geosite token="\"ext:geosite_user.dat:USERLIST\""
"ext:geosite_user.dat:USERLIST"],"outboundTag":"proxy","ruleTag":"proxy-domains-permanent","custom":{"keep":true}}]}}`)
	got, changed, err := PrepareUserGeosite(raw, filename)
	if err != nil {
		t.Fatal(err)
	}
	if changed || !bytes.Equal(got, raw) {
		t.Fatalf("correct managed rule changed (changed=%v):\n%s", changed, got)
	}
	if !strings.Contains(string(got), `"custom":{"keep":true}`) {
		t.Fatalf("unknown managed-rule member lost:\n%s", got)
	}
}

func TestPrepareUserGeositeDoesNotMigrateOldReference(t *testing.T) {
	oldReference := "ext:geosite_old.dat:USERLIST"
	raw := []byte(`{"routing":{"rules":[{"type":"field","domain":["` + oldReference + `"],"outboundTag":"proxy","ruleTag":"user-old"}]}}`)
	filename := "geosite_new.dat"
	got, changed, err := PrepareUserGeosite(raw, filename)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("new managed rule was not added")
	}
	if strings.Count(string(got), oldReference) != 1 {
		t.Fatalf("old reference was migrated or guessed:\n%s", got)
	}
	if !strings.Contains(string(got), `"ext:`+filename+`:USERLIST"`) {
		t.Fatalf("new reference missing:\n%s", got)
	}
	requireUserGeositeAnnotation(t, got, filename)
}

func TestPrepareUserGeositeRejectsAmbiguousOrConflictingRules(t *testing.T) {
	const (
		reference = "ext:geosite_user.dat:USERLIST"
		managed   = `"outboundTag":"proxy","ruleTag":"proxy-domains-permanent"`
	)
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "duplicate exact reference",
			raw: `{"routing":{"rules":[` +
				`{"type":"field","domain":["` + reference + `"],"outboundTag":"proxy"},` +
				`{"type":"field","domain":["` + reference + `"],"outboundTag":"proxy"}]}}`,
		},
		{
			name: "mixed domain conditions",
			raw:  `{"routing":{"rules":[{"type":"field","domain":["` + reference + `","example.com"],"outboundTag":"proxy"}]}}`,
		},
		{
			name: "wrong target",
			raw:  `{"routing":{"rules":[{"type":"field","domain":["` + reference + `"],"outboundTag":"direct"}]}}`,
		},
		{
			name: "extra condition",
			raw:  `{"routing":{"rules":[{"type":"field","domain":["` + reference + `"],"ip":["10.0.0.0/8"],"outboundTag":"proxy"}]}}`,
		},
		{
			name: "duplicate managed tag",
			raw: `{"routing":{"rules":[` +
				`{"type":"field","domain":["` + reference + `"],` + managed + `},` +
				`{"type":"field","domain":["` + reference + `"],` + managed + `}]}}`,
		},
		{
			name: "conflicting managed filename",
			raw:  `{"routing":{"rules":[{"type":"field","domain":["ext:geosite_other.dat:USERLIST"],` + managed + `}]}}`,
		},
		{
			name: "conflicting managed condition",
			raw:  `{"routing":{"rules":[{"type":"field","domain":["` + reference + `"],"ip":["10.0.0.0/8"],` + managed + `}]}}`,
		},
		{
			name: "conflicting annotation",
			raw: `{"routing":{"rules":[{"type":"field","domain":[
// helper:v1 bind user-geosite type=geosite-reference source=helper.user-geosite token="\"ext:geosite_other.dat:USERLIST\""
"` + reference + `"],` + managed + `}]}}`,
		},
		{
			name: "malformed annotation",
			raw: `{"routing":{"rules":[{"type":"field","domain":[
// helper: not a declaration
"` + reference + `"],` + managed + `}]}}`,
		},
		{
			name: "missing rules",
			raw:  `{"routing":{}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := PrepareUserGeosite([]byte(test.raw), "geosite_user.dat"); !errors.Is(err, ErrGateway) {
				t.Fatalf("error = %v; want ErrGateway", err)
			}
		})
	}
}

func TestPrepareUserGeositeRejectsUnsafeFilename(t *testing.T) {
	raw := []byte(`{"routing":{"rules":[]}}`)
	for _, filename := range []string{
		"../geosite_user.dat",
		"geosite.dat",
		"geoip_user.dat",
		"geosite_.dat",
		"geosite_user.txt",
		"geosite_user.dat\n",
	} {
		if _, _, err := PrepareUserGeosite(raw, filename); !errors.Is(err, ErrGateway) {
			t.Fatalf("filename %q: error = %v; want ErrGateway", filename, err)
		}
	}
}

func TestSetupGeositeAnchorsDynamicOverlayAfterSystemRules(t *testing.T) {
	raw := []byte(`{
 "api":{"tag":"api"},"outbounds":[{"tag":"dns-out","protocol":"dns"}],
 "routing":{"rules":[
 {"ruleTag":"system-api","inboundTag":["api"],"outboundTag":"api"},
 {"ruleTag":"system-dns","port":"53","outboundTag":"dns-out"},
 {"ruleTag":"system-private-direct","ip":["geoip:private"],"outboundTag":"direct"},
 {"ruleTag":"user-rule","domain":["domain:user.example"],"outboundTag":"direct"},
 {"ruleTag":"system-tproxy-default","inboundTag":["tproxy-in"],"outboundTag":"proxy"}
 ]}}`)
	prepared, _, err := PrepareUserGeosite(raw, "geosite_user.dat")
	if err != nil {
		t.Fatal(err)
	}
	reader := readerForContent(t, string(prepared))
	payload, err := reader.RoutingPayload(context.Background(), domain.BuildOverlay([]string{"added.example"}, []string{"removed.example"}))
	if err != nil {
		t.Fatal(err)
	}
	var routing map[string]any
	if err := json.Unmarshal(payload, &routing); err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, rule := range asSlice(routing["rules"]) {
		tags = append(tags, safeString(asMap(rule)["ruleTag"]))
	}
	want := []string{"system-api", "system-dns", "system-private-direct", domain.ExclusionsRuleTag, domain.AdditionsRuleTag, permanentProxyDomainsRuleTag, "user-rule", "system-tproxy-default"}
	if !reflect.DeepEqual(tags, want) {
		t.Fatalf("routing order: %v", tags)
	}
	for _, tag := range []string{domain.ExclusionsRuleTag, domain.AdditionsRuleTag} {
		if bytes.Contains(prepared, []byte(tag)) {
			t.Fatal("runtime rules persisted into setup config")
		}
	}
	if _, err := PlanDeleteRule(permanentProxyDomainsRuleTag)(prepared); err == nil {
		t.Fatal("managed anchor can be deleted through generic editor")
	}
}

func TestSetupGeositeRejectsDuplicateReferenceAlongsideManagedAnchor(t *testing.T) {
	raw := []byte(`{"routing":{"rules":[{"type":"field","domain":["ext:geosite_user.dat:USERLIST"],"outboundTag":"proxy","ruleTag":"proxy-domains-permanent"},{"domain":["ext:geosite_user.dat:USERLIST"],"outboundTag":"direct"}]}}`)
	if _, _, err := PrepareUserGeosite(raw, "geosite_user.dat"); !errors.Is(err, ErrGateway) {
		t.Fatal("duplicate reference accepted", err)
	}
}
