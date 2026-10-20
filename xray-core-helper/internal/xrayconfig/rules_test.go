package xrayconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/transaction"
)

const ruleFixture = `{
  "inbounds": [
    { "tag": "tproxy-in", "protocol": "dokodemo-door", "settings": { "followRedirect": true },
      "streamSettings": { "sockopt": { "tproxy": "tproxy" } } }
  ],
  "outbounds": [
    { "tag": "proxy", "protocol": "vless" },
    { "tag": "direct", "protocol": "freedom" },
    { "tag": "dns-out", "protocol": "dns" }
  ],
  "routing": {
    "balancers": [ { "tag": "bal", "selector": ["proxy"] } ],
    "rules": [
      { "ruleTag": "gateway", "inboundTag": ["tproxy-in"], "outboundTag": "proxy" },
      { "ruleTag": "dns", "port": "53", "outboundTag": "dns-out" },
      {
        // description of user-a
        "ruleTag": "user-a",
        "domain": ["a.example"],
        "outboundTag": "direct",
        "custom": { "keep": true }
      },
      { "ruleTag": "user-b", "ip": ["10.0.0.0/8"], "outboundTag": "direct" },
      { "ruleTag": "user-c", "domain": ["c.example"], "outboundTag": "direct" },
      { "domain": ["untagged.example"], "outboundTag": "direct" }
    ]
  }
}`

func ruleInput(tag string, domains ...string) api.RuleInput {
	return api.RuleInput{RuleTag: tag, Conditions: api.RuleConditions{Domain: domains}, OutboundTag: "direct"}
}

func ruleTags(t *testing.T, source string) []string {
	t.Helper()
	parsed, err := parseSource([]byte(source))
	if err != nil {
		t.Fatalf("result no longer parses: %v\n%s", err, source)
	}
	tags := make([]string, 0)
	for _, value := range configRules(parsed.value) {
		name, _ := asMap(value)["ruleTag"].(string)
		tags = append(tags, name)
	}
	return tags
}

func TestAddRuleAtPositionsAndRejections(t *testing.T) {
	for position, want := range map[int]string{
		0: "gateway,dns,user-a,user-b,user-c,,new",
		1: "new,gateway,dns,user-a,user-b,user-c,",
		4: "gateway,dns,user-a,new,user-b,user-c,",
	} {
		updated, err := plan(t, PlanAddRule(ruleInput("new", "n.example"), position), ruleFixture)
		if err != nil {
			t.Fatalf("position %d: %v", position, err)
		}
		if got := strings.Join(ruleTags(t, updated), ","); got != want {
			t.Errorf("position %d: tags = %s; want %s", position, got, want)
		}
	}
	tests := []struct {
		name  string
		input api.RuleInput
		pos   int
		code  string
	}{
		{"duplicate tag", ruleInput("user-a", "x.example"), 0, "rule_exists"},
		{"bad tag", ruleInput("!bad", "x.example"), 0, "invalid_rule_tag"},
		{"no condition", api.RuleInput{RuleTag: "n", OutboundTag: "direct"}, 0, "rule_conditions_required"},
		{"two targets", api.RuleInput{RuleTag: "n", Conditions: api.RuleConditions{Domain: []string{"x"}}, OutboundTag: "direct", BalancerTag: "bal"}, 0, "invalid_rule_target"},
		{"no target", api.RuleInput{RuleTag: "n", Conditions: api.RuleConditions{Domain: []string{"x"}}}, 0, "invalid_rule_target"},
		{"missing outbound", api.RuleInput{RuleTag: "n", Conditions: api.RuleConditions{Domain: []string{"x"}}, OutboundTag: "ghost"}, 0, "rule_target_not_found"},
		{"missing balancer", api.RuleInput{RuleTag: "n", Conditions: api.RuleConditions{Domain: []string{"x"}}, BalancerTag: "ghost"}, 0, "rule_target_not_found"},
		{"bad position", ruleInput("n", "x"), 9, "invalid_rule_position"},
		{"creates dns rule", api.RuleInput{RuleTag: "n", Conditions: api.RuleConditions{Domain: []string{"x"}}, OutboundTag: "dns-out"}, 0, "rule_protected"},
		{"creates tproxy rule", api.RuleInput{RuleTag: "n", Conditions: api.RuleConditions{InboundTag: []string{"tproxy-in"}}, OutboundTag: "direct"}, 0, "rule_protected"},
		{"long item", ruleInput("n", strings.Repeat("a", 513)), 0, "invalid_rule_condition"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := plan(t, PlanAddRule(test.input, test.pos), ruleFixture); rejectCode(err) != test.code {
				t.Fatalf("error = %v (%s); want %s", err, rejectCode(err), test.code)
			}
		})
	}
}

func TestAddRuleNormalizesConditions(t *testing.T) {
	input := api.RuleInput{
		RuleTag: "norm",
		Conditions: api.RuleConditions{
			Domain:  []string{" a.example ", "a.example", "", "b.example"},
			Port:    []string{"80", " 443 "},
			Network: []string{"tcp"},
		},
		OutboundTag: "direct",
	}
	updated, err := plan(t, PlanAddRule(input, 0), ruleFixture)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := parseSource([]byte(updated))
	rule := asMap(configRules(parsed.value)[6])
	if rule["port"] != "80,443" || rule["network"] != "tcp" || rule["type"] != "field" || len(asSlice(rule["domain"])) != 2 {
		t.Fatalf("rule = %#v", rule)
	}
}

func TestEditRulePreservesCommentsAndUnknownFields(t *testing.T) {
	input := api.RuleInput{RuleTag: "user-a", Conditions: api.RuleConditions{IP: []string{"geoip:private"}}, BalancerTag: "bal"}
	updated, err := plan(t, PlanEditRule("user-a", input), ruleFixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"// description of user-a", `"custom": { "keep": true }`, `"ruleTag": "user-b"`, `"ruleTag": "gateway"`} {
		if !strings.Contains(updated, kept) {
			t.Errorf("lost %q:\n%s", kept, updated)
		}
	}
	parsed, _ := parseSource([]byte(updated))
	rule := asMap(configRules(parsed.value)[2])
	if rule["domain"] != nil || rule["outboundTag"] != nil || rule["balancerTag"] != "bal" || len(asSlice(rule["ip"])) != 1 {
		t.Fatalf("edited rule = %#v", rule)
	}
	if trailingComma.MatchString(updated) {
		t.Errorf("trailing comma left:\n%s", updated)
	}
}

func TestEditRuleRenameAndNoOp(t *testing.T) {
	renamed, err := plan(t, PlanEditRule("user-b", api.RuleInput{RuleTag: "user-b2", Conditions: api.RuleConditions{IP: []string{"10.0.0.0/8"}}, OutboundTag: "direct"}), ruleFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(ruleTags(t, renamed), ","), "user-b2") {
		t.Fatalf("rename failed: %v", ruleTags(t, renamed))
	}
	same := api.RuleInput{RuleTag: "user-c", Conditions: api.RuleConditions{Domain: []string{"c.example"}}, OutboundTag: "direct"}
	edits, err := PlanEditRule("user-c", same)([]byte(ruleFixture))
	if err != nil || len(edits) != 0 {
		t.Fatalf("identical edit produced %d edits, error = %v", len(edits), err)
	}
}

func TestRuleMutationRejections(t *testing.T) {
	valid := ruleInput("renamed", "x.example")
	tests := []struct {
		name    string
		planner func() ([]byte, error)
		code    string
	}{
		{"edit tproxy", func() ([]byte, error) { return runPlanner(PlanEditRule("gateway", valid)) }, "rule_protected"},
		{"edit dns", func() ([]byte, error) { return runPlanner(PlanEditRule("dns", valid)) }, "rule_protected"},
		{"delete tproxy", func() ([]byte, error) { return runPlanner(PlanDeleteRule("gateway")) }, "rule_protected"},
		{"delete dns", func() ([]byte, error) { return runPlanner(PlanDeleteRule("dns")) }, "rule_protected"},
		{"move tproxy", func() ([]byte, error) { return runPlanner(PlanMoveRule("gateway", "down")) }, "rule_protected"},
		{"move user past dns", func() ([]byte, error) { return runPlanner(PlanMoveRule("user-a", "up")) }, "rule_protected"},
		{"edit missing", func() ([]byte, error) { return runPlanner(PlanEditRule("ghost", valid)) }, "rule_not_found"},
		{"delete missing", func() ([]byte, error) { return runPlanner(PlanDeleteRule("ghost")) }, "rule_not_found"},
		{"empty tag", func() ([]byte, error) { return runPlanner(PlanDeleteRule("")) }, "invalid_rule_tag"},
		{"edit into duplicate", func() ([]byte, error) { return runPlanner(PlanEditRule("user-b", ruleInput("user-c", "x"))) }, "rule_exists"},
		{"edit into tproxy", func() ([]byte, error) {
			return runPlanner(PlanEditRule("user-b", api.RuleInput{RuleTag: "user-b", Conditions: api.RuleConditions{InboundTag: []string{"tproxy-in"}}, OutboundTag: "direct"}))
		}, "rule_protected"},
		{"bad direction", func() ([]byte, error) { return runPlanner(PlanMoveRule("user-b", "left")) }, "invalid_direction"},
		{"move commented", func() ([]byte, error) { return runPlanner(PlanMoveRule("user-a", "down")) }, "rule_has_comments"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.planner(); rejectCode(err) != test.code {
				t.Fatalf("error = %v (%s); want %s", err, rejectCode(err), test.code)
			}
		})
	}
}

func runPlanner(planner transaction.Planner) ([]byte, error) {
	edits, err := planner([]byte(ruleFixture))
	if err != nil {
		return nil, err
	}
	return transaction.ApplyEdits([]byte(ruleFixture), edits)
}

func TestDeleteRuleKeepsNeighbours(t *testing.T) {
	updated, err := plan(t, PlanDeleteRule("user-b"), ruleFixture)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ruleTags(t, updated), ","); got != "gateway,dns,user-a,user-c," {
		t.Fatalf("tags = %s", got)
	}
	if !strings.Contains(updated, "// description of user-a") || trailingComma.MatchString(updated) {
		t.Fatalf("unexpected result:\n%s", updated)
	}
}

func TestMoveRuleSwapsUncommentedNeighbours(t *testing.T) {
	updated, err := plan(t, PlanMoveRule("user-b", "down"), ruleFixture)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ruleTags(t, updated), ","); got != "gateway,dns,user-a,user-c,user-b," {
		t.Fatalf("tags = %s", got)
	}
	up, err := plan(t, PlanMoveRule("user-c", "up"), ruleFixture)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ruleTags(t, up), ","); got != "gateway,dns,user-a,user-c,user-b," {
		t.Fatalf("tags after up = %s", got)
	}
	if !strings.Contains(updated, "// description of user-a") {
		t.Fatal("comment lost")
	}
}

func TestMoveRuleAtBoundaryIsRejected(t *testing.T) {
	source := `{"outbounds":[{"tag":"direct"}],"routing":{"rules":[
  {"ruleTag":"first","domain":["a"],"outboundTag":"direct"},
  {"ruleTag":"last","domain":["b"],"outboundTag":"direct"}]}}`
	for tag, direction := range map[string]string{"first": "up", "last": "down"} {
		if _, err := PlanMoveRule(tag, direction)([]byte(source)); rejectCode(err) != "rule_cannot_move" {
			t.Errorf("%s %s: error = %v", tag, direction, err)
		}
	}
}

func TestCloneUntaggedRuleIntoTaggedRule(t *testing.T) {
	updated, err := plan(t, PlanCloneRule(6, "cloned"), ruleFixture)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ruleTags(t, updated), ","); got != "gateway,dns,user-a,user-b,user-c,,cloned" {
		t.Fatalf("tags = %s", got)
	}
	parsed, _ := parseSource([]byte(updated))
	clone := asMap(configRules(parsed.value)[6])
	if len(asSlice(clone["domain"])) != 1 || clone["outboundTag"] != "direct" {
		t.Fatalf("clone = %#v", clone)
	}
	tests := []struct {
		name     string
		position int
		tag      string
		code     string
	}{
		{"protected", 1, "c1", "rule_protected"},
		{"dns", 2, "c2", "rule_protected"},
		{"duplicate", 6, "user-a", "rule_exists"},
		{"bad tag", 6, "", "invalid_rule_tag"},
		{"bad position", 9, "c3", "rule_not_found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := runPlanner(PlanCloneRule(test.position, test.tag)); rejectCode(err) != test.code {
				t.Fatalf("error = %v (%s); want %s", err, rejectCode(err), test.code)
			}
		})
	}
}

func TestRuleEditRunsInsideEngineAndRejectsStaleGeneration(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte(ruleFixture), 0640); err != nil {
		t.Fatal(err)
	}
	store, err := transaction.OpenStore(context.Background(), filepath.Join(directory, "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	engine, err := transaction.NewEngine(configPath, store, acceptAll{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(configPath)
	if _, err := engine.Mutate(context.Background(), 0, PlanDeleteRule("gateway"), false); rejectCode(err) != "rule_protected" {
		t.Fatalf("protected delete error = %v", err)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Fatal("rejected mutation changed the file")
	}
	if _, err := engine.Mutate(context.Background(), 3, PlanDeleteRule("user-b"), false); !errors.Is(err, transaction.ErrStaleGeneration) {
		t.Fatalf("stale error = %v", err)
	}
	metadata, err := engine.Mutate(context.Background(), 0, PlanMoveRule("user-b", "down"), false)
	if err != nil || metadata.Generation != 1 {
		t.Fatalf("Mutate() = %#v, %v", metadata, err)
	}
	final, _ := os.ReadFile(configPath)
	if got := strings.Join(ruleTags(t, string(final)), ","); got != "gateway,dns,user-a,user-c,user-b," {
		t.Fatalf("tags = %s", got)
	}
}
