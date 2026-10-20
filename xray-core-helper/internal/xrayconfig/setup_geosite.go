package xrayconfig

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/transaction"
)

const (
	userGeositeRuleTag  = permanentProxyDomainsRuleTag
	userGeositeCategory = "USERLIST"
)

var (
	userGeositeDeclaration  = regexp.MustCompile(`^// helper:v1 bind ([A-Za-z][A-Za-z0-9_-]{0,63}) type=geosite-reference source=helper\.user-geosite token=(".*")$`)
	userGeositeCategoryName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// PrepareUserGeosite ensures the single managed USERLIST routing rule exists.
// It only reuses an unmarked rule whose condition is exactly the requested
// reference and whose target is proxy; any ambiguity requires gateway review.
func PrepareUserGeosite(raw []byte, filename string) ([]byte, bool, error) {
	bound, err := BindUserGeosite(raw, filename)
	if err != nil {
		return nil, false, ErrGateway
	}
	candidate, _, err := prepareUserGeosite(bound, filename)
	if err != nil {
		return nil, false, err
	}
	return candidate, !bytes.Equal(raw, candidate), nil
}

func prepareUserGeosite(raw []byte, filename string) ([]byte, bool, error) {
	if !bootstrap.ValidUserGeositeName(filename) {
		return nil, false, ErrGateway
	}
	reference := userGeositeReference(filename)
	e, err := newEditor(raw)
	if err != nil {
		return nil, false, ErrGateway
	}
	rules, err := rulesArray(e.syntax)
	if err != nil {
		return nil, false, ErrGateway
	}

	tagged := make([]int, 0, 1)
	for index, value := range configRules(e.syntax.value) {
		if safeString(asMap(value)["ruleTag"]) == userGeositeRuleTag {
			tagged = append(tagged, index)
		}
	}

	if len(tagged) > 1 {
		return nil, false, ErrGateway
	}
	if len(tagged) == 1 {
		for index, value := range configRules(e.syntax.value) {
			if index == tagged[0] {
				continue
			}
			if domains, ok := userGeositeDomainValues(asMap(value)); ok && userGeositeContains(domains, reference) {
				return nil, false, ErrGateway
			}
		}
		return prepareTaggedUserGeosite(raw, e, rules, tagged[0], filename, reference)
	}
	return adoptOrInsertUserGeosite(raw, e, rules, filename, reference)
}

func prepareTaggedUserGeosite(raw []byte, e *editor, rules *jsonNode, index int, filename, reference string) ([]byte, bool, error) {
	rule := asMap(configRules(e.syntax.value)[index])
	if !userGeositeManagedShape(rule, reference) {
		return nil, false, ErrGateway
	}
	_, marked, markReference, markValid := userGeositeRuleMark(e, rules.array[index])
	switch {
	case marked && (!markValid || markReference != reference):
		return nil, false, ErrGateway
	case marked:
		if _, present := rule["type"]; present {
			return bytes.Clone(raw), false, nil
		}
	}

	work := newSession(raw)
	if _, present := rule["type"]; !present {
		if err := ensureUserGeositeMember(work, index, "type", "field"); err != nil {
			return nil, false, ErrGateway
		}
	}
	if !marked {
		if err := annotateUserGeositeRule(work, index, filename); err != nil {
			return nil, false, ErrGateway
		}
	}
	return finishUserGeosite(raw, work)
}

func adoptOrInsertUserGeosite(raw []byte, e *editor, rules *jsonNode, filename, reference string) ([]byte, bool, error) {
	exact := -1
	for index, value := range configRules(e.syntax.value) {
		domains, present := userGeositeDomainValues(asMap(value))
		if !present || !userGeositeContains(domains, reference) {
			continue
		}
		if len(domains) != 1 || domains[0] != reference {
			return nil, false, ErrGateway
		}
		if exact >= 0 {
			return nil, false, ErrGateway
		}
		exact = index
	}

	if exact >= 0 {
		rule := asMap(configRules(e.syntax.value)[exact])
		if !userGeositePlainRule(rule, reference) {
			return nil, false, ErrGateway
		}
		_, marked, markReference, markValid := userGeositeRuleMark(e, rules.array[exact])
		if marked && (!markValid || markReference != reference) {
			return nil, false, ErrGateway
		}
		work := newSession(raw)
		if _, present := rule["type"]; !present {
			if err := ensureUserGeositeMember(work, exact, "type", "field"); err != nil {
				return nil, false, ErrGateway
			}
		}
		if err := ensureUserGeositeMember(work, exact, "ruleTag", userGeositeRuleTag); err != nil {
			return nil, false, ErrGateway
		}
		if !marked {
			if err := annotateUserGeositeRule(work, exact, filename); err != nil {
				return nil, false, ErrGateway
			}
		}
		return finishUserGeosite(raw, work)
	}

	position := userGeositeInsertPosition(e.syntax.value, configRules(e.syntax.value))
	work := newSession(raw)
	err := work.apply(func(step *editor) ([]transaction.ByteEdit, error) {
		rule, err := rulesArray(step.syntax)
		if err != nil {
			return nil, err
		}
		edit, err := step.insertArrayItemAt(rule, position, userGeositeRule{
			Type:        "field",
			Domain:      []string{reference},
			OutboundTag: "proxy",
			RuleTag:     userGeositeRuleTag,
		})
		return []transaction.ByteEdit{edit}, err
	})
	if err != nil {
		return nil, false, ErrGateway
	}
	if err := annotateUserGeositeRule(work, position, filename); err != nil {
		return nil, false, ErrGateway
	}
	return finishUserGeosite(raw, work)
}

type userGeositeRule struct {
	Type        string   `json:"type"`
	Domain      []string `json:"domain"`
	OutboundTag string   `json:"outboundTag"`
	RuleTag     string   `json:"ruleTag"`
}

func finishUserGeosite(raw []byte, work *session) ([]byte, bool, error) {
	if _, err := parseSource(work.current); err != nil {
		return nil, false, ErrGateway
	}
	return bytes.Clone(work.current), !bytes.Equal(raw, work.current), nil
}

func userGeositeReference(filename string) string {
	return "ext:" + filename + ":" + userGeositeCategory
}

func userGeositeAnnotation(filename string) string {
	token := strconv.Quote(`"` + userGeositeReference(filename) + `"`)
	return `// helper:v1 bind user-geosite type=geosite-reference source=helper.user-geosite token=` + token
}

func userGeositeDomainValues(rule map[string]any) ([]string, bool) {
	value, present := rule["domain"]
	if !present {
		return nil, false
	}
	switch typed := value.(type) {
	case string:
		return []string{typed}, true
	case []string:
		return append([]string(nil), typed...), true
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, true
			}
			values = append(values, text)
		}
		return values, true
	default:
		return nil, true
	}
}

func userGeositeContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func userGeositeManagedShape(rule map[string]any, reference string) bool {
	if safeString(rule["outboundTag"]) != "proxy" || safeString(rule["ruleTag"]) != userGeositeRuleTag {
		return false
	}
	if kind, present := rule["type"]; present && safeString(kind) != "field" {
		return false
	}
	domains, present := userGeositeDomainValues(rule)
	if !present || len(domains) != 1 || domains[0] != reference {
		return false
	}
	for key := range rule {
		switch key {
		case "type", "domain", "outboundTag", "ruleTag":
		default:
			// Unknown members are preserved; additional routing conditions
			// would make the managed rule select more than its reference.
			if userGeositeConditionKey(key) {
				return false
			}
		}
	}
	return true
}

func userGeositeConditionKey(key string) bool {
	switch key {
	case "inboundTag", "ip", "port", "network", "protocol", "balancerTag",
		"source", "sourcePort", "user", "email", "domainMatcher", "attrs", "localIP", "localPort":
		return true
	}
	return false
}

func userGeositePlainRule(rule map[string]any, reference string) bool {
	if safeString(rule["outboundTag"]) != "proxy" {
		return false
	}
	if kind, present := rule["type"]; present && safeString(kind) != "field" {
		return false
	}
	domains, present := userGeositeDomainValues(rule)
	if !present || len(domains) != 1 || domains[0] != reference {
		return false
	}
	for key := range rule {
		switch key {
		case "type", "domain", "outboundTag":
		default:
			return false
		}
	}
	return true
}

func userGeositeRuleMark(e *editor, rule *jsonNode) (present bool, marked bool, reference string, valid bool) {
	scalar, ok := userGeositeDomainScalar(e, rule)
	if !ok {
		return false, false, "", false
	}
	line, present := userGeositeAnnotationBefore(e.source, scalar)
	if !present {
		return true, false, "", false
	}
	reference, valid = userGeositeParseAnnotation(line)
	return true, true, reference, valid
}

func userGeositeDomainScalar(e *editor, rule *jsonNode) (*jsonNode, bool) {
	if rule == nil || rule.object == nil {
		return nil, false
	}
	domain := rule.object["domain"]
	if domain == nil {
		return nil, false
	}
	if domain.array != nil {
		if len(domain.array) != 1 {
			return nil, false
		}
		return domain.array[0], true
	}
	if domain.object == nil && domain.start < len(e.source) && e.source[domain.start] == '"' {
		return domain, true
	}
	return nil, false
}

func userGeositeAnnotationBefore(source []byte, node *jsonNode) (string, bool) {
	if node == nil || node.start > len(source) {
		return "", false
	}
	lineStart := bytes.LastIndexByte(source[:node.start], '\n') + 1
	if !onlySpace(source[lineStart:node.start]) || lineStart == 0 {
		return "", false
	}
	end := lineStart - 1
	if end > 0 && source[end-1] == '\r' {
		end--
	}
	start := bytes.LastIndexByte(source[:end], '\n') + 1
	line := strings.TrimSpace(string(source[start:end]))
	if !strings.HasPrefix(line, "// helper:") {
		return "", false
	}
	return line, true
}

func userGeositeParseAnnotation(line string) (string, bool) {
	match := userGeositeDeclaration.FindStringSubmatch(line)
	if match == nil {
		return "", false
	}
	var token string
	if err := json.Unmarshal([]byte(match[2]), &token); err != nil {
		return "", false
	}
	if len(token) < 2 || token[0] != '"' || token[len(token)-1] != '"' {
		return "", false
	}
	reference, err := strconv.Unquote(token)
	if err != nil {
		return "", false
	}
	filename, category, ok := userGeositeReferenceParts(reference)
	if !ok || filename == "" || category != userGeositeCategory {
		return "", false
	}
	return reference, true
}

func userGeositeReferenceParts(reference string) (filename, category string, ok bool) {
	remainder, found := strings.CutPrefix(reference, "ext:")
	if !found {
		return "", "", false
	}
	filename, category, found = strings.Cut(remainder, ":")
	if !found || !bootstrap.ValidUserGeositeName(filename) || !userGeositeCategoryName.MatchString(category) {
		return "", "", false
	}
	return filename, category, true
}

func ensureUserGeositeMember(work *session, index int, key string, value any) error {
	return work.apply(func(e *editor) ([]transaction.ByteEdit, error) {
		rules, err := rulesArray(e.syntax)
		if err != nil {
			return nil, err
		}
		if index < 0 || index >= len(rules.array) {
			return nil, ErrGateway
		}
		node := rules.array[index]
		if node.object[key] != nil {
			return nil, nil
		}
		edit, err := e.appendObjectMember(node, key, value)
		return []transaction.ByteEdit{edit}, err
	})
}

func annotateUserGeositeRule(work *session, index int, filename string) error {
	return work.apply(func(e *editor) ([]transaction.ByteEdit, error) {
		rules, err := rulesArray(e.syntax)
		if err != nil {
			return nil, err
		}
		if index < 0 || index >= len(rules.array) {
			return nil, ErrGateway
		}
		domain := rules.array[index].object["domain"]
		scalar, ok := userGeositeDomainScalar(e, rules.array[index])
		if domain == nil || !ok {
			return nil, ErrGateway
		}
		lineStart := bytes.LastIndexByte(e.source[:scalar.start], '\n') + 1
		indent := e.lineIndent(scalar.start)
		prefix := ""
		if !onlySpace(e.source[lineStart:scalar.start]) {
			// Inline arrays need their own declaration line before the scalar.
			indent = e.lineIndent(domain.start) + "  "
			prefix = "\n" + indent
		}
		original := string(e.source[scalar.start:scalar.end])
		replacement := prefix + userGeositeAnnotation(filename) + "\n" + indent + original
		return []transaction.ByteEdit{{
			Start: scalar.start, End: scalar.end, Replacement: []byte(replacement),
		}}, nil
	})
}

// userGeositeInsertPosition skips only the initial API, DNS and private/local
// direct rules. It stops at the first user rule or the tproxy fallback.
func userGeositeInsertPosition(config map[string]any, rules []any) int {
	apiTag := safeString(asMap(config["api"])["tag"])
	if apiTag == "" {
		apiTag = "api"
	}
	dnsTags := make(map[string]bool)
	for _, value := range asSlice(config["outbounds"]) {
		outbound := asMap(value)
		if safeString(outbound["protocol"]) == "dns" {
			if tag := safeString(outbound["tag"]); tag != "" {
				dnsTags[tag] = true
			}
		}
	}
	for index, value := range rules {
		rule := asMap(value)
		if safeString(rule["ruleTag"]) == "system-tproxy-default" {
			return index
		}
		if userGeositeAPIRule(rule, apiTag) || userGeositeDNSRule(rule, dnsTags) || userGeositePrivateDirect(rule) {
			continue
		}
		return index
	}
	return len(rules)
}

func userGeositeAPIRule(rule map[string]any, apiTag string) bool {
	if safeString(rule["ruleTag"]) == "system-api" {
		return true
	}
	if safeString(rule["outboundTag"]) == apiTag {
		return true
	}
	return userGeositeHasTag(rule["inboundTag"], apiTag)
}

func userGeositeDNSRule(rule map[string]any, dnsTags map[string]bool) bool {
	if dnsTags[safeString(rule["outboundTag"])] {
		return true
	}
	if tag := safeString(rule["ruleTag"]); tag == "system-dns" {
		return true
	}
	for _, tag := range stringValues(rule["inboundTag"]) {
		lower := strings.ToLower(tag)
		if lower == "dns" || strings.HasPrefix(lower, "dns-") {
			return true
		}
	}
	return false
}

func userGeositePrivateDirect(rule map[string]any) bool {
	if safeString(rule["outboundTag"]) != "direct" {
		return false
	}
	switch safeString(rule["ruleTag"]) {
	case "system-private-direct", "hook-local":
		return true
	}
	for _, field := range []string{"ip", "domain", "source"} {
		for _, item := range stringValues(rule[field]) {
			switch strings.ToLower(strings.TrimSpace(item)) {
			case "geoip:private", "geosite:private", "geoip:lan", "geosite:lan", "private", "local", "localhost", "domain:localhost":
				return true
			}
		}
	}
	return false
}

func userGeositeHasTag(value any, tag string) bool {
	for _, candidate := range stringValues(value) {
		if candidate == tag {
			return true
		}
	}
	return false
}
