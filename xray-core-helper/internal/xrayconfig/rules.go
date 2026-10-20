package xrayconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/transaction"
)

const (
	maxRuleItems      = 256
	maxRuleItemLength = 512
)

var (
	ruleTagPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,63}$`)
	controlledRuleKeys = []string{"ruleTag", "inboundTag", "domain", "ip", "port", "network", "protocol", "outboundTag", "balancerTag"}
)

// protectionResolver reports why a rule belongs to the gateway profile, or "".
func protectionResolver(config map[string]any) func(map[string]any) string {
	transparent := make(map[string]struct{})
	for _, value := range asSlice(config["inbounds"]) {
		inbound := asMap(value)
		if inbound["protocol"] != "dokodemo-door" {
			continue
		}
		settings := asMap(inbound["settings"])
		sockopt := asMap(asMap(inbound["streamSettings"])["sockopt"])
		if settings["followRedirect"] == true || truthy(sockopt["tproxy"]) {
			if tag := safeString(inbound["tag"]); tag != "" {
				transparent[tag] = struct{}{}
			}
		}
	}
	dnsTags := map[string]struct{}{"dns-out": {}}
	for _, value := range asSlice(config["outbounds"]) {
		outbound := asMap(value)
		if outbound["protocol"] == "dns" {
			if tag := safeString(outbound["tag"]); tag != "" {
				dnsTags[tag] = struct{}{}
			}
		}
	}
	return func(rule map[string]any) string {
		if safeString(rule["ruleTag"]) == permanentProxyDomainsRuleTag {
			return "Managed proxy-domain rule"
		}
		for _, tag := range stringValues(rule["inboundTag"]) {
			if _, protected := transparent[tag]; protected {
				return "TProxy gateway rule"
			}
		}
		if _, protected := dnsTags[safeString(rule["outboundTag"])]; protected {
			return "DNS gateway rule"
		}
		return ""
	}
}

func rulesArray(syntax *parsedSource) (*jsonNode, error) {
	if routing := syntax.spans.object["routing"]; routing != nil {
		if rules := routing.object["rules"]; rules != nil && rules.array != nil {
			return rules, nil
		}
	}
	return nil, transaction.Reject("invalid_config", "Xray configuration has no routing.rules array", 422)
}

func configRules(config map[string]any) []any {
	return asSlice(asMap(config["routing"])["rules"])
}

func findRuleByTag(config map[string]any, tag string) (int, error) {
	if tag == "" {
		return -1, transaction.Reject("invalid_rule_tag", "rule tag is required", 400)
	}
	found := -1
	for index, value := range configRules(config) {
		if name, _ := asMap(value)["ruleTag"].(string); name == tag {
			if found >= 0 {
				return -1, transaction.Reject("rule_tag_ambiguous", fmt.Sprintf("rule tag '%s' is used by several rules", tag), 409)
			}
			found = index
		}
	}
	if found < 0 {
		return -1, transaction.Reject("rule_not_found", fmt.Sprintf("rule '%s' was not found", tag), 404)
	}
	return found, nil
}

func ensureEditable(rule map[string]any, protection func(map[string]any) string, action string) error {
	if reason := protection(rule); reason != "" {
		return transaction.Reject("rule_protected", fmt.Sprintf("%s cannot be %s", reason, action), 403)
	}
	if kind, present := rule["type"]; present {
		if text, _ := kind.(string); text != "field" {
			return transaction.Reject("rule_unsupported", "only routing rules of type 'field' are supported", 400)
		}
	}
	return nil
}

func cleanList(field string, items []string) ([]string, error) {
	seen := make(map[string]struct{})
	cleaned := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if len(item) > maxRuleItemLength {
			return nil, transaction.Reject("invalid_rule_condition", field+" contains an item longer than 512 characters", 400)
		}
		if _, duplicate := seen[item]; !duplicate {
			seen[item] = struct{}{}
			cleaned = append(cleaned, item)
		}
	}
	if len(cleaned) > maxRuleItems {
		return nil, transaction.Reject("invalid_rule_condition", field+" contains too many items", 400)
	}
	return cleaned, nil
}

func tagExists(list any, tag string) bool {
	for _, value := range asSlice(list) {
		if name, _ := asMap(value)["tag"].(string); name == tag {
			return true
		}
	}
	return false
}

// buildRuleFields validates input and returns the controlled rule members; exclude is the edited index.
func buildRuleFields(input api.RuleInput, config map[string]any, exclude int) (map[string]any, error) {
	tag := strings.TrimSpace(input.RuleTag)
	if !ruleTagPattern.MatchString(tag) {
		return nil, transaction.Reject("invalid_rule_tag", "rule tag must contain 1-64 letters, numbers, spaces, dots, underscores, or dashes", 400)
	}
	for index, value := range configRules(config) {
		if name, _ := asMap(value)["ruleTag"].(string); index != exclude && name == tag {
			return nil, transaction.Reject("rule_exists", fmt.Sprintf("rule '%s' already exists", tag), 409)
		}
	}
	fields := map[string]any{"ruleTag": tag}
	conditions := []struct {
		key  string
		join bool
		list []string
	}{
		{"inboundTag", false, input.Conditions.InboundTag},
		{"domain", false, input.Conditions.Domain},
		{"ip", false, input.Conditions.IP},
		{"port", true, input.Conditions.Port},
		{"network", true, input.Conditions.Network},
		{"protocol", false, input.Conditions.Protocol},
	}
	count := 0
	for _, condition := range conditions {
		cleaned, err := cleanList(condition.key, condition.list)
		if err != nil {
			return nil, err
		}
		if len(cleaned) == 0 {
			continue
		}
		count++
		if condition.join {
			fields[condition.key] = strings.Join(cleaned, ",")
		} else {
			fields[condition.key] = cleaned
		}
	}
	if count == 0 {
		return nil, transaction.Reject("rule_conditions_required", "at least one routing condition is required", 400)
	}
	outbound, balancer := strings.TrimSpace(input.OutboundTag), strings.TrimSpace(input.BalancerTag)
	if (outbound != "") == (balancer != "") {
		return nil, transaction.Reject("invalid_rule_target", "select exactly one outbound or balancer target", 400)
	}
	if outbound != "" {
		if !tagExists(config["outbounds"], outbound) {
			return nil, transaction.Reject("rule_target_not_found", fmt.Sprintf("outbound '%s' was not found", outbound), 400)
		}
		fields["outboundTag"] = outbound
	} else {
		if !tagExists(asMap(config["routing"])["balancers"], balancer) {
			return nil, transaction.Reject("rule_target_not_found", fmt.Sprintf("balancer '%s' was not found", balancer), 400)
		}
		fields["balancerTag"] = balancer
	}
	return fields, nil
}

func mergeRule(existing map[string]any, fields map[string]any) map[string]any {
	rule := make(map[string]any, len(existing)+len(fields))
	for key, value := range existing {
		rule[key] = value
	}
	for _, key := range controlledRuleKeys {
		delete(rule, key)
	}
	for key, value := range fields {
		rule[key] = value
	}
	return rule
}

func sameJSON(left, right any) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func deepCopy(rule map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(rule)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var copied map[string]any
	if err := decoder.Decode(&copied); err != nil {
		return nil, err
	}
	return copied, nil
}

func hasComments(e *editor, node *jsonNode) bool {
	return !bytes.Equal(e.source[node.start:node.end], e.syntax.clean[node.start:node.end])
}

// PlanAddRule inserts a tagged user rule; position is 1-based, 0 appends.
func PlanAddRule(input api.RuleInput, position int) transaction.Planner {
	return func(source []byte) ([]transaction.ByteEdit, error) {
		e, err := newEditor(source)
		if err != nil {
			return nil, err
		}
		rules, err := rulesArray(e.syntax)
		if err != nil {
			return nil, err
		}
		fields, err := buildRuleFields(input, e.syntax.value, -1)
		if err != nil {
			return nil, err
		}
		rule := mergeRule(map[string]any{"type": "field"}, fields)
		if reason := protectionResolver(e.syntax.value)(rule); reason != "" {
			return nil, transaction.Reject("rule_protected", reason+" cannot be created through the editor", 403)
		}
		count := len(rules.array)
		if position == 0 {
			position = count + 1
		}
		if position < 1 || position > count+1 {
			return nil, transaction.Reject("invalid_rule_position", fmt.Sprintf("rule position must be between 1 and %d", count+1), 400)
		}
		edit, err := e.insertArrayItemAt(rules, position-1, rule)
		if err != nil {
			return nil, err
		}
		return []transaction.ByteEdit{edit}, nil
	}
}

// PlanEditRule rewrites only the controlled members of the rule identified by tag.
func PlanEditRule(tag string, input api.RuleInput) transaction.Planner {
	return func(source []byte) ([]transaction.ByteEdit, error) {
		e, err := newEditor(source)
		if err != nil {
			return nil, err
		}
		if _, err := rulesArray(e.syntax); err != nil {
			return nil, err
		}
		index, err := findRuleByTag(e.syntax.value, tag)
		if err != nil {
			return nil, err
		}
		existing := asMap(configRules(e.syntax.value)[index])
		protection := protectionResolver(e.syntax.value)
		if err := ensureEditable(existing, protection, "edited"); err != nil {
			return nil, err
		}
		fields, err := buildRuleFields(input, e.syntax.value, index)
		if err != nil {
			return nil, err
		}
		if reason := protection(mergeRule(existing, fields)); reason != "" {
			return nil, transaction.Reject("rule_protected", reason+" cannot be created through the editor", 403)
		}
		work := newSession(source)
		for _, key := range controlledRuleKeys {
			desired, wanted := fields[key]
			err := work.apply(func(step *editor) ([]transaction.ByteEdit, error) {
				rules, err := rulesArray(step.syntax)
				if err != nil {
					return nil, err
				}
				node := rules.array[index]
				member := node.object[key]
				switch {
				case wanted && member != nil:
					if sameJSON(asMap(configRules(step.syntax.value)[index])[key], desired) {
						return nil, nil
					}
					edit, err := step.replaceNode(member, desired)
					return []transaction.ByteEdit{edit}, err
				case wanted:
					edit, err := step.appendObjectMember(node, key, desired)
					return []transaction.ByteEdit{edit}, err
				case member != nil:
					return step.deleteMembers(node, map[string]bool{key: true}), nil
				}
				return nil, nil
			})
			if err != nil {
				return nil, err
			}
		}
		return work.edits(), nil
	}
}

// PlanDeleteRule removes the editable rule identified by tag.
func PlanDeleteRule(tag string) transaction.Planner {
	return func(source []byte) ([]transaction.ByteEdit, error) {
		e, err := newEditor(source)
		if err != nil {
			return nil, err
		}
		rules, err := rulesArray(e.syntax)
		if err != nil {
			return nil, err
		}
		index, err := findRuleByTag(e.syntax.value, tag)
		if err != nil {
			return nil, err
		}
		if err := ensureEditable(asMap(configRules(e.syntax.value)[index]), protectionResolver(e.syntax.value), "removed"); err != nil {
			return nil, err
		}
		return e.deleteArrayItem(rules, index)
	}
}

// PlanMoveRule swaps an editable rule with its neighbour when no comments can be detached.
func PlanMoveRule(tag, direction string) transaction.Planner {
	return func(source []byte) ([]transaction.ByteEdit, error) {
		if direction != "up" && direction != "down" {
			return nil, transaction.Reject("invalid_direction", "direction must be 'up' or 'down'", 400)
		}
		e, err := newEditor(source)
		if err != nil {
			return nil, err
		}
		rules, err := rulesArray(e.syntax)
		if err != nil {
			return nil, err
		}
		index, err := findRuleByTag(e.syntax.value, tag)
		if err != nil {
			return nil, err
		}
		target := index + 1
		if direction == "up" {
			target = index - 1
		}
		if target < 0 || target >= len(rules.array) {
			return nil, transaction.Reject("rule_cannot_move", fmt.Sprintf("rule '%s' cannot move %s", tag, direction), 409)
		}
		protection := protectionResolver(e.syntax.value)
		list := configRules(e.syntax.value)
		for _, candidate := range []int{index, target} {
			if err := ensureEditable(asMap(list[candidate]), protection, "reordered"); err != nil {
				return nil, err
			}
		}
		for _, candidate := range []int{index, target} {
			if hasComments(e, rules.array[candidate]) {
				return nil, transaction.Reject("rule_has_comments", "commented routing rules cannot be reordered safely", 409)
			}
		}
		first, second := rules.array[index], rules.array[target]
		return []transaction.ByteEdit{
			{Start: first.start, End: first.end, Replacement: append([]byte(nil), e.source[second.start:second.end]...)},
			{Start: second.start, End: second.end, Replacement: append([]byte(nil), e.source[first.start:first.end]...)},
		}, nil
	}
}

// PlanCloneRule copies the rule at the 1-based position into a new tagged rule right after it.
func PlanCloneRule(position int, newTag string) transaction.Planner {
	return func(source []byte) ([]transaction.ByteEdit, error) {
		e, err := newEditor(source)
		if err != nil {
			return nil, err
		}
		rules, err := rulesArray(e.syntax)
		if err != nil {
			return nil, err
		}
		if position < 1 || position > len(rules.array) {
			return nil, transaction.Reject("rule_not_found", fmt.Sprintf("rule position must be between 1 and %d", len(rules.array)), 404)
		}
		tag := strings.TrimSpace(newTag)
		if !ruleTagPattern.MatchString(tag) {
			return nil, transaction.Reject("invalid_rule_tag", "rule tag must contain 1-64 letters, numbers, spaces, dots, underscores, or dashes", 400)
		}
		for _, value := range configRules(e.syntax.value) {
			if name, _ := asMap(value)["ruleTag"].(string); name == tag {
				return nil, transaction.Reject("rule_exists", fmt.Sprintf("rule '%s' already exists", tag), 409)
			}
		}
		original := asMap(configRules(e.syntax.value)[position-1])
		if err := ensureEditable(original, protectionResolver(e.syntax.value), "cloned"); err != nil {
			return nil, err
		}
		clone, err := deepCopy(original)
		if err != nil {
			return nil, err
		}
		clone["ruleTag"] = tag
		edit, err := e.insertArrayItemAt(rules, position, clone)
		if err != nil {
			return nil, err
		}
		return []transaction.ByteEdit{edit}, nil
	}
}
