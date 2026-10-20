package domain

import "sort"

// BuildResourceOverlay computes a deterministic overlay from desired and
// baseline resources. Values are classified and normalized with
// SplitResources; domains and IPv4 addresses are diffed independently. Any
// invalid value rejects the entire input, returning a zero Overlay.
func BuildResourceOverlay(desired, baseline []string) (Overlay, error) {
	desiredDomains, desiredIPv4, err := SplitResources(desired)
	if err != nil {
		return Overlay{}, err
	}
	baselineDomains, baselineIPv4, err := SplitResources(baseline)
	if err != nil {
		return Overlay{}, err
	}

	domainAdditions := subtractResources(desiredDomains, baselineDomains)
	domainExclusions := subtractResources(baselineDomains, desiredDomains)
	ipv4Additions := subtractResources(desiredIPv4, baselineIPv4)
	ipv4Exclusions := subtractResources(baselineIPv4, desiredIPv4)

	overlay := Overlay{
		Additions:  make([]string, 0, len(domainAdditions)+len(ipv4Additions)),
		Exclusions: make([]string, 0, len(domainExclusions)+len(ipv4Exclusions)),
		Rules:      make([]ManagedRule, 0, 4),
	}
	overlay.Additions = append(overlay.Additions, domainAdditions...)
	overlay.Additions = append(overlay.Additions, ipv4Additions...)
	overlay.Exclusions = append(overlay.Exclusions, domainExclusions...)
	overlay.Exclusions = append(overlay.Exclusions, ipv4Exclusions...)
	sort.Strings(overlay.Additions)
	sort.Strings(overlay.Exclusions)

	if len(domainExclusions) > 0 {
		overlay.Rules = append(overlay.Rules, managedRule(domainExclusions, "direct", ExclusionsRuleTag))
	}
	if len(ipv4Exclusions) > 0 {
		overlay.Rules = append(overlay.Rules, ipv4ManagedRule(ipv4Exclusions, "direct", IPv4ExclusionsRuleTag))
	}
	if len(domainAdditions) > 0 {
		overlay.Rules = append(overlay.Rules, managedRule(domainAdditions, "proxy", AdditionsRuleTag))
	}
	if len(ipv4Additions) > 0 {
		overlay.Rules = append(overlay.Rules, ipv4ManagedRule(ipv4Additions, "proxy", IPv4AdditionsRuleTag))
	}

	return overlay, nil
}

func subtractResources(values, other []string) []string {
	otherSet := make(map[string]struct{}, len(other))
	for _, value := range other {
		otherSet[value] = struct{}{}
	}

	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := otherSet[value]; !exists {
			result = append(result, value)
		}
	}
	return result
}

func ipv4ManagedRule(addresses []string, outboundTag, ruleTag string) ManagedRule {
	conditions := make([]string, len(addresses))
	copy(conditions, addresses)
	return ManagedRule{
		Type:        "field",
		IP:          conditions,
		OutboundTag: outboundTag,
		RuleTag:     ruleTag,
	}
}
