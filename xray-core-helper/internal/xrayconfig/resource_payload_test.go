package xrayconfig

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"home-router/xray-core-helper/internal/domain"
)

const resourceRoutingFixture = `{"routing":{"domainStrategy":"AsIs","unknown":"preserve","rules":[
 {"ruleTag":"system-private-direct","ip":["geoip:private"],"outboundTag":"direct"},
 {"ruleTag":"dynamic-proxy-ips","ip":["192.0.2.100"],"outboundTag":"proxy"},
 {"ruleTag":"dynamic-proxy-ip-exclusions","ip":["192.0.2.101"],"outboundTag":"direct"},
 {"ruleTag":"proxy-domains-permanent","domain":["ext:geosite_user.dat:USERLIST"],"outboundTag":"proxy"},
 {"ruleTag":"proxy-ip-permanent","ip":["ext:geoip_user.dat:USERLIST"],"outboundTag":"proxy"},
 {"ruleTag":"user-policy","port":"443","outboundTag":"direct"}
]}}`

func TestResourcePayloadKeepsSystemAndPermanentPriority(t *testing.T) {
	reader := readerForContent(t, resourceRoutingFixture)
	overlay, err := domain.BuildResourceOverlay([]string{"new.example", "192.0.2.1"}, []string{"old.example", "192.0.2.2"})
	if err != nil {
		t.Fatal(err)
	}
	// Payload construction owns the order even if its caller supplies rules in
	// another order. Both types of exclusions must precede additions.
	for i, j := 0, len(overlay.Rules)-1; i < j; i, j = i+1, j-1 {
		overlay.Rules[i], overlay.Rules[j] = overlay.Rules[j], overlay.Rules[i]
	}
	first, err := reader.RoutingPayload(context.Background(), overlay)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.RoutingPayload(context.Background(), overlay)
	if err != nil || string(second) != string(first) {
		t.Fatal("repeated overlay payload not stable", err)
	}
	var result struct {
		Strategy string `json:"domainStrategy"`
		Unknown  string `json:"unknown"`
		Rules    []struct {
			RuleTag string   `json:"ruleTag"`
			Domain  []string `json:"domain"`
			IP      []string `json:"ip"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(first, &result); err != nil {
		t.Fatal(err)
	}
	tags := []string{}
	for _, rule := range result.Rules {
		tags = append(tags, rule.RuleTag)
	}
	if !reflect.DeepEqual(tags, []string{"system-private-direct", domain.ExclusionsRuleTag, domain.IPv4ExclusionsRuleTag, domain.AdditionsRuleTag, domain.IPv4AdditionsRuleTag, "proxy-domains-permanent", "proxy-ip-permanent", "user-policy"}) {
		t.Fatal("wrong resource/system rule priority", tags)
	}
	if result.Strategy != "AsIs" || result.Unknown != "preserve" {
		t.Fatal("routing fields lost")
	}
	if !reflect.DeepEqual(result.Rules[2].IP, []string{"192.0.2.2"}) || len(result.Rules[2].Domain) != 0 || !reflect.DeepEqual(result.Rules[4].IP, []string{"192.0.2.1"}) {
		t.Fatal("IP conditions serialized as domains or stale IP rules retained")
	}
}

func TestResourcePayloadRejectsMissingDuplicateOrEarlierPermanentIPRule(t *testing.T) {
	overlay, err := domain.BuildResourceOverlay([]string{"192.0.2.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		`{"routing":{"rules":[{"ruleTag":"proxy-domains-permanent"}]}}`,
		`{"routing":{"rules":[{"ruleTag":"proxy-ip-permanent"},{"ruleTag":"proxy-domains-permanent"}]}}`,
		`{"routing":{"rules":[{"ruleTag":"proxy-domains-permanent"},{"ruleTag":"proxy-ip-permanent"},{"ruleTag":"proxy-ip-permanent"}]}}`,
	} {
		reader := readerForContent(t, content)
		if _, err := reader.RoutingPayload(context.Background(), overlay); err == nil {
			t.Fatal("unsafe IP priority accepted")
		}
	}
}

func TestResourcePayloadRejectsInvalidTypedOverlayRules(t *testing.T) {
	reader := readerForContent(t, resourceRoutingFixture)
	for _, rule := range []domain.ManagedRule{
		{Type: "field", IP: []string{"192.0.2.1"}, OutboundTag: "direct", RuleTag: domain.IPv4AdditionsRuleTag},
		{Type: "field", IP: []string{"example.com"}, OutboundTag: "proxy", RuleTag: domain.IPv4AdditionsRuleTag},
		{Type: "field", IP: []string{"::1"}, OutboundTag: "proxy", RuleTag: domain.IPv4AdditionsRuleTag},
		{Type: "field", IP: []string{"192.0.2.0/24"}, OutboundTag: "proxy", RuleTag: domain.IPv4AdditionsRuleTag},
		{Type: "field", Domain: []string{"domain:192.0.2.1"}, OutboundTag: "proxy", RuleTag: domain.AdditionsRuleTag},
		{Type: "field", Domain: []string{"domain:example.com"}, IP: []string{"192.0.2.1"}, OutboundTag: "proxy", RuleTag: domain.AdditionsRuleTag},
		{Type: "field", Domain: []string{"domain:example.com"}, OutboundTag: "proxy", RuleTag: "unknown"},
		{Type: "field", OutboundTag: "proxy", RuleTag: domain.IPv4AdditionsRuleTag},
	} {
		if _, err := reader.RoutingPayload(context.Background(), domain.Overlay{Rules: []domain.ManagedRule{rule}}); err == nil {
			t.Fatal("invalid typed overlay accepted")
		}
	}
}
