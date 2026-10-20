package xrayconfig

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/domain"
)

const routingPayloadFixture = `{
  // JSONC must remain untouched by payload extraction.
  "routing": {
    "domainStrategy":"IPIfNonMatch",
    "unknownRoutingField":{"preserve":["one","two"]},
    "rules":[
      {"ruleTag":"ordinary-before","domain":["geosite:example"]},
      {"ruleTag":"dynamic-proxy-domains","domain":["domain:stale-addition"]},
      {"ruleTag":"proxy-domains-permanent","domain":["geosite:proxy-domains"]},
      {"ruleTag":"ordinary-after","port":"443"},
      {"ruleTag":"dynamic-proxy-domain-exclusions","domain":["domain:stale-exclusion"]}
    ],
    "balancers":[
      {"tag":"auto","selector":["proxy"],"fallbackTag":"direct","unknown":"kept"}
    ],
    "unknownRoutingTopLevel":{"token":"retained"}
  }
}`

func TestRoutingPayloadPreservesCompleteRoutingAndInsertsOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	before := []byte(routingPayloadFixture)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(Settings{Config: path})
	overlay := domain.BuildOverlay(
		[]string{"kept.example", "new.example"},
		[]string{"kept.example", "removed.example"},
	)

	payload, err := reader.RoutingPayload(context.Background(), overlay)
	if err != nil {
		t.Fatal(err)
	}
	var routing map[string]any
	if err := json.Unmarshal(payload, &routing); err != nil {
		t.Fatal(err)
	}
	wrapped, err := json.Marshal(struct {
		Routing RoutingPayload `json:"routing"`
	}{Routing: payload})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wrapped), `"routing":{`) {
		t.Fatalf("routing payload was not serialized as a JSON object: %s", wrapped)
	}
	if routing["domainStrategy"] != "IPIfNonMatch" ||
		!reflect.DeepEqual(routing["unknownRoutingField"], map[string]any{"preserve": []any{"one", "two"}}) ||
		!reflect.DeepEqual(routing["unknownRoutingTopLevel"], map[string]any{"token": "retained"}) {
		t.Fatalf("routing fields were lost: %s", payload)
	}
	rules, ok := routing["rules"].([]any)
	if !ok {
		t.Fatalf("routing.rules = %#v", routing["rules"])
	}
	gotTags := make([]string, 0, len(rules))
	for _, value := range rules {
		rule, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("routing rule = %#v", value)
		}
		gotTags = append(gotTags, safeString(rule["ruleTag"]))
	}
	wantTags := []string{
		"ordinary-before",
		domain.ExclusionsRuleTag,
		domain.AdditionsRuleTag,
		"proxy-domains-permanent",
		"ordinary-after",
	}
	if !reflect.DeepEqual(gotTags, wantTags) {
		t.Fatalf("rule order = %#v; want %#v", gotTags, wantTags)
	}
	if !reflect.DeepEqual(rules[0].(map[string]any)["domain"], []any{"geosite:example"}) ||
		!reflect.DeepEqual(rules[4].(map[string]any)["port"], "443") {
		t.Fatalf("ordinary routing rules were changed: %#v", rules)
	}
	balancers, ok := routing["balancers"].([]any)
	if !ok || !reflect.DeepEqual(balancers, []any{
		map[string]any{"tag": "auto", "selector": []any{"proxy"}, "fallbackTag": "direct", "unknown": "kept"},
	}) {
		t.Fatalf("balancers not preserved: %#v", routing["balancers"])
	}
	if !reflect.DeepEqual(readFile(t, path), before) {
		t.Fatal("routing payload extraction modified the on-disk JSONC")
	}
}

func TestRoutingPayloadDoesNotRequireTProxyOrDNSRules(t *testing.T) {
	reader := readerForContent(t, `{"routing":{"rules":[{"ruleTag":"proxy-domains-permanent"}]}}`)
	payload, err := reader.RoutingPayload(context.Background(), domain.Overlay{})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded["rules"].([]any)) != 1 {
		t.Fatalf("routing payload = %s", payload)
	}
}

func TestRoutingPayloadRejectsMissingPermanentRuleAndMalformedConfig(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "missing permanent proxy-domain rule",
			content: `{"routing":{"rules":[
				{"ruleTag":"ordinary"},
				{"ruleTag":"dynamic-proxy-domains"}
			]}}`,
		},
		{
			name:    "malformed JSONC",
			content: `{"password":"fixture-secret", /* incomplete`,
		},
		{
			name:    "invalid rules structure",
			content: `{"routing":{"rules":"fixture-secret"}}`,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			reader := readerForContent(t, testCase.content)
			_, err := reader.RoutingPayload(context.Background(), domain.Overlay{})
			if err == nil {
				t.Fatal("RoutingPayload() succeeded; want safe configuration error")
			}
			code, message, status := ErrorInfo(err)
			if code != "invalid_config" || status != 422 || strings.Contains(message, "fixture-secret") {
				t.Fatalf("error = (%q, %q, %d); want safe invalid_config", code, message, status)
			}
		})
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
