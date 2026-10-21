package xrayconfig

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"home-router/xray-core-helper/internal/transaction"
)

var (
	outboundTagPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	protectedOutboundTag = map[string]struct{}{"proxy": {}, "direct": {}, "block": {}, "dns-out": {}}
)

func outboundsArray(syntax *parsedSource) (*jsonNode, error) {
	node := syntax.spans.object["outbounds"]
	if node == nil || node.array == nil {
		return nil, transaction.Reject("invalid_config", "Xray configuration has no outbounds array", 422)
	}
	return node, nil
}

func validateOutboundTag(tag string) error {
	if !outboundTagPattern.MatchString(tag) {
		return transaction.Reject("invalid_outbound_tag", "outbound tag must contain 1-64 letters, numbers, dots, underscores, or dashes", 400)
	}
	return nil
}

func findOutbound(syntax *parsedSource, tag string) (int, map[string]any) {
	for index, value := range asSlice(syntax.value["outbounds"]) {
		outbound := asMap(value)
		if name, _ := outbound["tag"].(string); name == tag {
			return index, outbound
		}
	}
	return -1, nil
}

func PlanDeleteOutbound(tag string) transaction.Planner {
	return func(source []byte) ([]transaction.ByteEdit, error) {
		if err := validateOutboundTag(tag); err != nil {
			return nil, err
		}
		e, err := newEditor(source)
		if err != nil {
			return nil, err
		}
		array, err := outboundsArray(e.syntax)
		if err != nil {
			return nil, err
		}
		index, _ := findOutbound(e.syntax, tag)
		if index < 0 {
			return nil, transaction.Reject("outbound_not_found", fmt.Sprintf("outbound '%s' was not found", tag), 404)
		}
		if _, reserved := protectedOutboundTag[tag]; reserved {
			return nil, transaction.Reject("outbound_protected",
				fmt.Sprintf("outbound '%s' is required by the gateway profile and cannot be removed", tag), 403)
		}
		if references := outboundReferences(e.syntax.value, tag); len(references) > 0 {
			return nil, transaction.Reject("outbound_in_use",
				fmt.Sprintf("outbound '%s' is used by routing: %s", tag, strings.Join(references, ", ")), 409)
		}
		return e.deleteArrayItem(array, index)
	}
}

// outboundReferences lists routing paths using tag; balancer selectors match by prefix as in Xray.
func outboundReferences(config map[string]any, tag string) []string {
	routing := asMap(config["routing"])
	references := make([]string, 0)
	for index, value := range asSlice(routing["rules"]) {
		if name, _ := asMap(value)["outboundTag"].(string); name == tag {
			references = append(references, fmt.Sprintf("routing.rules[%d].outboundTag", index))
		}
	}
	for index, value := range asSlice(routing["balancers"]) {
		balancer := asMap(value)
		for _, selector := range asSlice(balancer["selector"]) {
			if prefix, _ := selector.(string); prefix != "" && strings.HasPrefix(tag, prefix) {
				references = append(references, fmt.Sprintf("routing.balancers[%d].selector", index))
				break
			}
		}
		if fallback, _ := balancer["fallbackTag"].(string); fallback == tag {
			references = append(references, fmt.Sprintf("routing.balancers[%d].fallbackTag", index))
		}
	}
	for index, item := range asSlice(config["outbounds"]) {
		out := asMap(item)
		if asMap(asMap(out["streamSettings"])["sockopt"])["dialerProxy"] == tag {
			references = append(references, fmt.Sprintf("outbounds[%d].streamSettings.sockopt.dialerProxy", index))
		}
		if asMap(out["proxySettings"])["tag"] == tag {
			references = append(references, fmt.Sprintf("outbounds[%d].proxySettings.tag", index))
		}
	}
	sort.Strings(references)
	return references
}
