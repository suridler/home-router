package xrayconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"home-router/xray-core-helper/internal/domain"
)

const permanentProxyDomainsRuleTag = "proxy-domains-permanent"

type RoutingPayload = json.RawMessage

func (reader Reader) RoutingPayload(ctx context.Context, overlay domain.Overlay) (RoutingPayload, error) {
	doc, err := reader.readDocument(ctx)
	if err != nil {
		return nil, err
	}
	routing, ok := doc.value["routing"].(map[string]any)
	if !ok {
		return nil, invalidRoutingPayload("Xray configuration has no routing object")
	}
	rules, ok := routing["rules"].([]any)
	if !ok {
		return nil, invalidRoutingPayload("Xray configuration has no routing.rules array")
	}

	payload := make(map[string]any, len(routing))
	for key, value := range routing {
		payload[key] = value
	}
	payloadRules := make([]any, 0, len(rules)+len(overlay.Rules))
	permanentIndex := -1
	permanentIPIndex, permanentIPCount := -1, 0
	for _, value := range rules {
		rule, ok := value.(map[string]any)
		if !ok {
			return nil, invalidRoutingPayload("Xray configuration contains an invalid routing rule")
		}
		tag := safeString(rule["ruleTag"])
		if managedOverlayTag(tag) {
			continue
		}
		if tag == permanentProxyDomainsRuleTag {
			if permanentIndex >= 0 {
				return nil, invalidRoutingPayload("Xray configuration contains duplicate permanent proxy-domain rules")
			}
			permanentIndex = len(payloadRules)
		}
		if tag == "proxy-ip-permanent" {
			permanentIPCount++
			permanentIPIndex = len(payloadRules)
		}
		payloadRules = append(payloadRules, value)
	}
	if permanentIndex < 0 {
		return nil, invalidRoutingPayload("Xray configuration is missing the permanent proxy-domain rule")
	}
	managedByTag := make(map[string]domain.ManagedRule, len(overlay.Rules))
	for _, rule := range overlay.Rules {
		if !validManagedOverlayRule(rule) {
			return nil, invalidRoutingPayload("routing overlay contains an unsupported managed rule")
		}
		if len(rule.IP) > 0 && (permanentIPCount != 1 || permanentIPIndex <= permanentIndex) {
			return nil, invalidRoutingPayload("routing overlay requires the permanent IP rule after the permanent domain rule")
		}
		if _, duplicate := managedByTag[rule.RuleTag]; duplicate {
			return nil, invalidRoutingPayload("routing overlay contains duplicate managed rules")
		}
		managedByTag[rule.RuleTag] = rule
	}
	managedRules := make([]any, 0, len(managedByTag))
	for _, tag := range []string{domain.ExclusionsRuleTag, domain.IPv4ExclusionsRuleTag, domain.AdditionsRuleTag, domain.IPv4AdditionsRuleTag} {
		if rule, exists := managedByTag[tag]; exists {
			managedRules = append(managedRules, rule)
		}
	}
	insertedRules := make([]any, len(payloadRules)+len(managedRules))
	copy(insertedRules, payloadRules[:permanentIndex])
	copy(insertedRules[permanentIndex:], managedRules)
	copy(insertedRules[permanentIndex+len(managedRules):], payloadRules[permanentIndex:])
	payload["rules"] = insertedRules

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode routing payload: %w", err)
	}
	return RoutingPayload(encoded), nil
}

func managedOverlayTag(tag string) bool {
	switch tag {
	case domain.AdditionsRuleTag, domain.ExclusionsRuleTag, domain.IPv4AdditionsRuleTag, domain.IPv4ExclusionsRuleTag:
		return true
	}
	return false
}

func validManagedOverlayRule(rule domain.ManagedRule) bool {
	if !managedOverlayTag(rule.RuleTag) || rule.Type != "field" {
		return false
	}
	expected := "proxy"
	if rule.RuleTag == domain.ExclusionsRuleTag || rule.RuleTag == domain.IPv4ExclusionsRuleTag {
		expected = "direct"
	}
	if rule.OutboundTag != expected {
		return false
	}
	if rule.RuleTag == domain.IPv4AdditionsRuleTag || rule.RuleTag == domain.IPv4ExclusionsRuleTag {
		if len(rule.IP) == 0 || len(rule.Domain) != 0 {
			return false
		}
		for _, value := range rule.IP {
			normalized, kind, err := domain.NormalizeResource(value)
			if err != nil || kind != domain.IPv4Resource || normalized != value {
				return false
			}
		}
		return true
	}
	if len(rule.Domain) == 0 || len(rule.IP) != 0 {
		return false
	}
	for _, value := range rule.Domain {
		name, found := strings.CutPrefix(value, "domain:")
		normalized, kind, err := domain.NormalizeResource(name)
		if !found || err != nil || kind != domain.DomainResource || normalized != name {
			return false
		}
	}
	return true
}

func invalidRoutingPayload(message string) error {
	return &projectionError{code: "invalid_config", message: message, status: 422}
}
