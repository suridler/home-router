package domain

import "sort"

const (
	AdditionsRuleTag      = "dynamic-proxy-domains"
	ExclusionsRuleTag     = "dynamic-proxy-domain-exclusions"
	IPv4AdditionsRuleTag  = "dynamic-proxy-ips"
	IPv4ExclusionsRuleTag = "dynamic-proxy-ip-exclusions"
)

type ManagedRule struct {
	Type        string   `json:"type"`
	Domain      []string `json:"domain,omitempty"`
	IP          []string `json:"ip,omitempty"`
	OutboundTag string   `json:"outboundTag"`
	RuleTag     string   `json:"ruleTag"`
}

type Overlay struct {
	Additions  []string      `json:"additions"`
	Exclusions []string      `json:"exclusions"`
	Rules      []ManagedRule `json:"rules"`
}

func BuildOverlay(desired, baseline []string) Overlay {
	desiredSet := make(map[string]struct{}, len(desired))
	baselineSet := make(map[string]struct{}, len(baseline))
	for _, name := range desired {
		desiredSet[name] = struct{}{}
	}
	for _, name := range baseline {
		baselineSet[name] = struct{}{}
	}

	overlay := Overlay{
		Additions:  make([]string, 0),
		Exclusions: make([]string, 0),
		Rules:      make([]ManagedRule, 0, 2),
	}
	for name := range desiredSet {
		if _, exists := baselineSet[name]; !exists {
			overlay.Additions = append(overlay.Additions, name)
		}
	}
	for name := range baselineSet {
		if _, exists := desiredSet[name]; !exists {
			overlay.Exclusions = append(overlay.Exclusions, name)
		}
	}
	sort.Strings(overlay.Additions)
	sort.Strings(overlay.Exclusions)

	if len(overlay.Exclusions) > 0 {
		overlay.Rules = append(overlay.Rules, managedRule(overlay.Exclusions, "direct", ExclusionsRuleTag))
	}
	if len(overlay.Additions) > 0 {
		overlay.Rules = append(overlay.Rules, managedRule(overlay.Additions, "proxy", AdditionsRuleTag))
	}
	return overlay
}

func managedRule(domains []string, outboundTag, ruleTag string) ManagedRule {
	conditions := make([]string, len(domains))
	for index, name := range domains {
		conditions[index] = "domain:" + name
	}
	return ManagedRule{
		Type:        "field",
		Domain:      conditions,
		OutboundTag: outboundTag,
		RuleTag:     ruleTag,
	}
}
