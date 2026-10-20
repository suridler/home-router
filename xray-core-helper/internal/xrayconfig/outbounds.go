package xrayconfig

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/vless"
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

func parseVlessForEdit(uri string, key vless.TransportKey) (vless.Parsed, error) {
	parsed, err := vless.Parse(uri, key)
	if err != nil {
		return vless.Parsed{}, transaction.Reject("invalid_vless_uri", err.Error(), 400)
	}
	if names := parsed.Preview.UnsupportedParameters; len(names) > 0 {
		return vless.Parsed{}, transaction.Reject("unsupported_vless_parameters",
			"unsupported VLESS URI parameters: "+strings.Join(names, ", "), 400)
	}
	return parsed, nil
}

// PlanAddVlessOutbound inherits the current native egress mark, not a default.
func PlanAddVlessOutbound(tag, uri string) transaction.Planner {
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
		if index, _ := findOutbound(e.syntax, tag); index >= 0 {
			return nil, transaction.Reject("outbound_exists", fmt.Sprintf("outbound '%s' already exists", tag), 409)
		}
		if _, reserved := protectedOutboundTag[tag]; reserved {
			return nil, transaction.Reject("outbound_protected", fmt.Sprintf("outbound tag '%s' is reserved by the gateway profile", tag), 403)
		}
		if tag == "vless" {
			return nil, transaction.Reject("invalid_outbound_tag", "outbound tag 'vless' collides with an API route", 400)
		}
		parsed, err := parseVlessForEdit(uri, vless.TransportKeyNetwork)
		if err != nil {
			return nil, err
		}
		mark := 0
		for _, value := range asSlice(e.syntax.value["outbounds"]) {
			sock := asMap(asMap(asMap(value)["streamSettings"])["sockopt"])
			if raw, exists := sock["mark"]; exists {
				n, ok := markInteger(raw)
				if !ok || (mark != 0 && mark != n) {
					return nil, transaction.Reject("egress_mark_ambiguous", "native egress mark is ambiguous", 422)
				}
				mark = n
			}
		}
		if mark == 0 {
			return nil, transaction.Reject("egress_mark_ambiguous", "native egress mark is unavailable", 422)
		}
		parsed.StreamSettings["sockopt"] = map[string]any{"mark": mark}
		edit, err := e.appendArrayItem(array, map[string]any{
			"tag":            tag,
			"protocol":       "vless",
			"settings":       parsed.Settings,
			"streamSettings": parsed.StreamSettings,
		})
		if err != nil {
			return nil, err
		}
		return []transaction.ByteEdit{edit}, nil
	}
}

// PlanUpdateVlessOutbound replaces settings and streamSettings of an existing VLESS outbound.
func PlanUpdateVlessOutbound(tag, uri string) transaction.Planner {
	return func(source []byte) ([]transaction.ByteEdit, error) {
		if err := validateOutboundTag(tag); err != nil {
			return nil, err
		}
		e, err := newEditor(source)
		if err != nil {
			return nil, err
		}
		if _, err := outboundsArray(e.syntax); err != nil {
			return nil, err
		}
		index, outbound := findOutbound(e.syntax, tag)
		if index < 0 {
			return nil, transaction.Reject("outbound_not_found", fmt.Sprintf("outbound '%s' was not found", tag), 404)
		}
		if protocol, _ := outbound["protocol"].(string); protocol != "vless" {
			return nil, transaction.Reject("outbound_not_vless", fmt.Sprintf("outbound '%s' is not VLESS", tag), 400)
		}
		existingStream := asMap(outbound["streamSettings"])
		key := vless.TransportKeyNetwork
		if _, hasMethod := existingStream["method"]; hasMethod {
			key = vless.TransportKeyMethod
		}
		parsed, err := parseVlessForEdit(uri, key)
		if err != nil {
			return nil, err
		}
		if sockopt, ok := existingStream["sockopt"]; ok {
			parsed.StreamSettings["sockopt"] = sockopt
		}
		work := newSession(source)
		for _, member := range []struct {
			name  string
			value any
		}{{"settings", parsed.Settings}, {"streamSettings", parsed.StreamSettings}} {
			err := work.apply(func(step *editor) ([]transaction.ByteEdit, error) {
				array, err := outboundsArray(step.syntax)
				if err != nil {
					return nil, err
				}
				node := array.array[index]
				var edit transaction.ByteEdit
				if current := node.object[member.name]; current != nil {
					edit, err = step.replaceNode(current, member.value)
				} else {
					edit, err = step.appendObjectMember(node, member.name, member.value)
				}
				return []transaction.ByteEdit{edit}, err
			})
			if err != nil {
				return nil, err
			}
		}
		return work.edits(), nil
	}
}

// PlanDeleteOutbound removes an unprotected, unreferenced outbound.
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
	sort.Strings(references)
	return references
}
