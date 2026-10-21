package routingservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"home-router/xray-core-helper/internal/domain"
	"home-router/xray-core-helper/internal/xrayconfig"
)

const resourceRulePrefix = "helper-proxy-resource-"

// ApplyRouting reconciles only helper-owned resource rules. The reader's full
// projection is used for configuration validation, never sent to Xray: static
// rules and their geodata matchers stay in the running process unchanged.
func (client *Client) ApplyRouting(ctx context.Context, payload xrayconfig.RoutingPayload) error {
	desired, err := resourceRules(payload)
	if err != nil {
		return err
	}
	current, err := client.resourceTags(ctx)
	if err != nil {
		return err
	}
	var missing []domain.ManagedRule
	wanted := make(map[string]bool, len(desired))
	for _, rule := range desired {
		wanted[rule.RuleTag] = true
		if !current[rule.RuleTag] {
			missing = append(missing, rule)
		}
	}
	if len(missing) > 0 {
		encoded, err := json.Marshal(struct {
			Rules []domain.ManagedRule `json:"rules"`
		}{missing})
		if err != nil {
			return errors.New("encode resource routing rules")
		}
		if err := client.appendRouting(ctx, encoded); err != nil {
			// Append can succeed even when its response is lost. Check tags before
			// returning a failure; a future apply also converges from actual runtime.
			actual, readErr := client.resourceTags(ctx)
			if readErr != nil {
				return err
			}
			for _, rule := range missing {
				if !actual[rule.RuleTag] {
					return err
				}
			}
		}
	}
	// Add first, then remove obsolete tags. Unchanged resources are never removed.
	obsolete := make([]string, 0)
	for tag := range current {
		if ownedResourceTag(tag) && !wanted[tag] {
			obsolete = append(obsolete, tag)
		}
	}
	sort.Strings(obsolete)
	for _, tag := range obsolete {
		if err := client.RemoveRule(ctx, tag); err != nil {
			return err
		}
	}
	return nil
}

func (client *Client) resourceTags(ctx context.Context) (map[string]bool, error) {
	raw, err := client.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	var response struct {
		Rules *[]struct {
			RuleTag string `json:"ruleTag"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || response.Rules == nil {
		return nil, errors.New("invalid Xray routing rule list")
	}
	tags := make(map[string]bool, len(*response.Rules))
	for _, rule := range *response.Rules {
		tags[rule.RuleTag] = true
	}
	return tags, nil
}

func ownedResourceTag(tag string) bool {
	switch tag {
	case domain.AdditionsRuleTag, domain.IPv4AdditionsRuleTag, domain.ExclusionsRuleTag, domain.IPv4ExclusionsRuleTag:
		return true
	}
	suffix, ok := strings.CutPrefix(tag, resourceRulePrefix)
	if !ok || len(suffix) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil && suffix == strings.ToLower(suffix)
}

func resourceRules(payload xrayconfig.RoutingPayload) ([]domain.ManagedRule, error) {
	var projection struct {
		Rules []json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(payload, &projection); err != nil {
		return nil, errors.New("invalid resource routing payload")
	}
	rules := make([]domain.ManagedRule, 0)
	seen := make(map[string]bool)
	for _, raw := range projection.Rules {
		var header struct {
			RuleTag string `json:"ruleTag"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return nil, errors.New("invalid resource routing rule")
		}
		if header.RuleTag != domain.AdditionsRuleTag && header.RuleTag != domain.IPv4AdditionsRuleTag {
			continue
		}
		var group domain.ManagedRule
		if err := json.Unmarshal(raw, &group); err != nil || group.Type != "field" || group.OutboundTag != "proxy" {
			return nil, errors.New("invalid managed resource group")
		}
		values := group.Domain
		ipRule := group.RuleTag == domain.IPv4AdditionsRuleTag
		if ipRule {
			if len(group.Domain) != 0 || len(group.IP) == 0 {
				return nil, errors.New("invalid managed IP group")
			}
			values = group.IP
		} else if len(group.IP) != 0 || len(group.Domain) == 0 {
			return nil, errors.New("invalid managed domain group")
		}
		for _, value := range values {
			resource := value
			if !ipRule {
				var ok bool
				resource, ok = strings.CutPrefix(value, "domain:")
				if !ok {
					return nil, errors.New("invalid resource domain")
				}
			}
			normalized, kind, err := domain.NormalizeResource(resource)
			if err != nil || normalized != resource || (ipRule && kind != domain.IPv4Resource) || (!ipRule && kind != domain.DomainResource) {
				return nil, errors.New("invalid proxy resource")
			}
			rule := domain.ManagedRule{Type: "field", OutboundTag: "proxy"}
			if ipRule {
				rule.IP = []string{value}
			} else {
				rule.Domain = []string{value}
			}
			encoded, _ := json.Marshal(rule)
			rule.RuleTag = fmt.Sprintf("%s%x", resourceRulePrefix, sha256.Sum256(encoded))
			if !seen[rule.RuleTag] {
				rules = append(rules, rule)
				seen[rule.RuleTag] = true
			}
		}
	}
	return rules, nil
}
