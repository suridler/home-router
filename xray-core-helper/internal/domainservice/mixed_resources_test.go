package domainservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/domain"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/xrayconfig"
)

const mixedConfigJSONC = `{
  // Domain and IPv4 overlays must leave this JSONC exactly as written.
  "log": {"loglevel":"warning"},
  "routing": {
    "domainStrategy":"AsIs",
    "unknownRoutingField":{"keep":true},
    "rules": [
      {"ruleTag":"system-api","ip":["geoip:private"],"outboundTag":"direct"},
      {"ruleTag":"proxy-domains-permanent","domain":["ext:geosite_user.dat:USERLIST"],"outboundTag":"proxy"},
      {"ruleTag":"proxy-ip-permanent","ip":["ext:geoip_user.dat:USERLIST"],"outboundTag":"proxy"},
      {"ruleTag":"user-default","port":"443","outboundTag":"direct"}
    ]
  }
}`

type mixedRule struct {
	RuleTag     string   `json:"ruleTag"`
	Domain      []string `json:"domain"`
	IP          []string `json:"ip"`
	OutboundTag string   `json:"outboundTag"`
}

func mixedRules(t *testing.T, payload xrayconfig.RoutingPayload) []mixedRule {
	t.Helper()
	var routing struct {
		Rules []mixedRule `json:"rules"`
	}
	if err := json.Unmarshal(payload, &routing); err != nil {
		t.Fatalf("decode mixed routing payload: %v", err)
	}
	return routing.Rules
}

func mixedTags(rules []mixedRule) []string {
	tags := make([]string, len(rules))
	for index, rule := range rules {
		tags[index] = rule.RuleTag
	}
	return tags
}

func mixedRuleByTag(t *testing.T, rules []mixedRule, tag string) mixedRule {
	t.Helper()
	for _, rule := range rules {
		if rule.RuleTag == tag {
			return rule
		}
	}
	t.Fatalf("missing rule %q in %v", tag, mixedTags(rules))
	return mixedRule{}
}

func mixedRuleIndex(t *testing.T, rules []mixedRule, tag string) int {
	t.Helper()
	for index, rule := range rules {
		if rule.RuleTag == tag {
			return index
		}
	}
	t.Fatalf("missing rule %q in %v", tag, mixedTags(rules))
	return -1
}

func TestMixedAddKeepsDomainAndIPv4RulesSeparateAndDeterministic(t *testing.T) {
	ctx := context.Background()
	client := &runtimeClient{}
	service, store, configPath := newService(t, mixedConfigJSONC, client)
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := service.Add(ctx, "  Plus.Example.  "); err != nil {
		t.Fatalf("Add(domain) = %v", err)
	}
	if err := service.Add(ctx, " 192.0.2.10 "); err != nil {
		t.Fatalf("Add(IPv4) = %v", err)
	}

	wantSnapshot := domainstore.Snapshot{
		Desired:  []string{"192.0.2.10", "plus.example"},
		Baseline: []string{},
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(snapshot, wantSnapshot) {
		t.Fatalf("Snapshot() = %#v, %v; want canonical mixed snapshot %#v", snapshot, err, wantSnapshot)
	}

	payloads, _ := client.recorded()
	if len(payloads) != 2 {
		t.Fatalf("runtime applies after two Adds = %d; want one per Add", len(payloads))
	}
	last := payloads[len(payloads)-1]
	rules := mixedRules(t, last)
	wantTags := []string{
		"system-api",
		domain.AdditionsRuleTag,
		domain.IPv4AdditionsRuleTag,
		"proxy-domains-permanent",
		"proxy-ip-permanent",
		"user-default",
	}
	if got := mixedTags(rules); !reflect.DeepEqual(got, wantTags) {
		t.Fatalf("mixed rule order = %v; want %v", got, wantTags)
	}
	domainRule := mixedRuleByTag(t, rules, domain.AdditionsRuleTag)
	ipRule := mixedRuleByTag(t, rules, domain.IPv4AdditionsRuleTag)
	if !reflect.DeepEqual(domainRule.Domain, []string{"domain:plus.example"}) ||
		len(domainRule.IP) != 0 || domainRule.OutboundTag != "proxy" {
		t.Fatalf("domain additions rule = %#v; want only domain: condition", domainRule)
	}
	if !reflect.DeepEqual(ipRule.IP, []string{"192.0.2.10"}) ||
		len(ipRule.Domain) != 0 || ipRule.OutboundTag != "proxy" {
		t.Fatalf("IPv4 additions rule = %#v; want only IP condition", ipRule)
	}
	if strings.Contains(string(last), "domain:192.0.2.10") {
		t.Fatalf("IPv4 was serialized as a domain condition: %s", last)
	}

	for range 2 {
		if err := service.Apply(ctx); err != nil {
			t.Fatalf("repeated Apply() = %v", err)
		}
	}
	payloads, _ = client.recorded()
	if len(payloads) != 4 {
		t.Fatalf("runtime applies after repeated Apply = %d; want four", len(payloads))
	}
	if !reflect.DeepEqual(payloads[1], payloads[2]) || !reflect.DeepEqual(payloads[2], payloads[3]) {
		t.Fatal("repeated mixed Apply produced different payloads")
	}
	if snapshot, err = store.Snapshot(ctx); err != nil || !reflect.DeepEqual(snapshot, wantSnapshot) {
		t.Fatalf("Snapshot() after repeated Apply = %#v, %v; want %#v", snapshot, err, wantSnapshot)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("mixed domain service modified Xray JSONC")
	}
}

func TestMixedBaselineIPReplacementOrdersExclusionAdditionsAndPermanentRules(t *testing.T) {
	ctx := context.Background()
	client := &runtimeClient{}
	service, store, _ := newService(t, mixedConfigJSONC, client)

	for _, value := range []string{"baseline.example", "192.0.2.20"} {
		if err := store.AddDesired(ctx, value); err != nil {
			t.Fatalf("seed desired %q: %v", value, err)
		}
	}
	frozen, err := store.BeginConsolidation(ctx)
	if err != nil || !frozen.ConsolidationActive {
		t.Fatalf("BeginConsolidation() = %#v, %v; want active freeze", frozen, err)
	}
	if err := store.FinalizeConsolidation(ctx, frozen.Desired); err != nil {
		t.Fatalf("FinalizeConsolidation(%v) = %v", frozen.Desired, err)
	}
	baseline := domainstore.Snapshot{
		Desired:  []string{"192.0.2.20", "baseline.example"},
		Baseline: []string{"192.0.2.20", "baseline.example"},
	}
	if snapshot, err := store.Snapshot(ctx); err != nil || !reflect.DeepEqual(snapshot, baseline) {
		t.Fatalf("seeded Snapshot() = %#v, %v; want %#v", snapshot, err, baseline)
	}

	if err := service.Delete(ctx, "192.0.2.20"); err != nil {
		t.Fatalf("Delete(baseline IPv4) = %v", err)
	}
	if err := service.Add(ctx, "192.0.2.21"); err != nil {
		t.Fatalf("Add(new IPv4) = %v", err)
	}

	wantSnapshot := domainstore.Snapshot{
		Desired:  []string{"192.0.2.21", "baseline.example"},
		Baseline: []string{"192.0.2.20", "baseline.example"},
	}
	if snapshot, err := store.Snapshot(ctx); err != nil || !reflect.DeepEqual(snapshot, wantSnapshot) {
		t.Fatalf("replaced Snapshot() = %#v, %v; want %#v", snapshot, err, wantSnapshot)
	}

	payloads, _ := client.recorded()
	if len(payloads) != 2 {
		t.Fatalf("runtime applies after IP replacement = %d; want one per mutation", len(payloads))
	}
	last := payloads[len(payloads)-1]
	rules := mixedRules(t, last)
	wantTags := []string{
		"system-api",
		domain.IPv4ExclusionsRuleTag,
		domain.IPv4AdditionsRuleTag,
		"proxy-domains-permanent",
		"proxy-ip-permanent",
		"user-default",
	}
	if got := mixedTags(rules); !reflect.DeepEqual(got, wantTags) {
		t.Fatalf("IP replacement rule order = %v; want %v", got, wantTags)
	}
	exclusion := mixedRuleByTag(t, rules, domain.IPv4ExclusionsRuleTag)
	additions := mixedRuleByTag(t, rules, domain.IPv4AdditionsRuleTag)
	if !reflect.DeepEqual(exclusion.IP, []string{"192.0.2.20"}) ||
		len(exclusion.Domain) != 0 || exclusion.OutboundTag != "direct" {
		t.Fatalf("IP exclusion rule = %#v; want direct baseline IP only", exclusion)
	}
	if !reflect.DeepEqual(additions.IP, []string{"192.0.2.21"}) ||
		len(additions.Domain) != 0 || additions.OutboundTag != "proxy" {
		t.Fatalf("IP additions rule = %#v; want proxy replacement IP only", additions)
	}
	exclusionIndex := mixedRuleIndex(t, rules, domain.IPv4ExclusionsRuleTag)
	additionsIndex := mixedRuleIndex(t, rules, domain.IPv4AdditionsRuleTag)
	permanentDomainIndex := mixedRuleIndex(t, rules, "proxy-domains-permanent")
	permanentIPIndex := mixedRuleIndex(t, rules, "proxy-ip-permanent")
	if !(exclusionIndex < additionsIndex && additionsIndex < permanentDomainIndex && permanentDomainIndex < permanentIPIndex) {
		t.Fatalf("IP exclusion/additions must precede permanent rules: %v", mixedTags(rules))
	}
	for _, forbidden := range []string{"domain:192.0.2.20", "domain:192.0.2.21"} {
		if strings.Contains(string(last), forbidden) {
			t.Fatalf("routing payload serialized IPv4 as a domain condition %q: %s", forbidden, last)
		}
	}
}

func TestRuntimeReconcilerReplaysMixedOverlayAcrossTokenRestartAndTransientFailure(t *testing.T) {
	ctx := context.Background()
	client := &runtimeClient{}
	service, store, _ := newService(t, mixedConfigJSONC, client)
	for _, value := range []string{"keep.example", "192.0.2.30"} {
		if err := store.AddDesired(ctx, value); err != nil {
			t.Fatalf("seed desired %q: %v", value, err)
		}
	}
	identity := &identityFake{token: "runtime-a"}
	reconciler, err := NewRuntimeReconciler(service, identity.Identity)
	if err != nil {
		t.Fatal(err)
	}

	if changed, err := reconciler.Reconcile(ctx); err != nil || !changed {
		t.Fatalf("first Reconcile() = %t, %v; want mixed overlay applied", changed, err)
	}
	identity.setToken("runtime-b")
	if changed, err := reconciler.Reconcile(ctx); err != nil || !changed {
		t.Fatalf("changed-token Reconcile() = %t, %v; want mixed overlay reapplied", changed, err)
	}

	fresh, err := NewRuntimeReconciler(service, identity.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := fresh.Reconcile(ctx); err != nil || !changed {
		t.Fatalf("fresh-helper Reconcile() = %t, %v; want mixed overlay replayed", changed, err)
	}

	payloads, _ := client.recorded()
	if len(payloads) != 3 {
		t.Fatalf("runtime applies after token change and restart = %d; want three", len(payloads))
	}
	wantTags := []string{
		"system-api",
		domain.AdditionsRuleTag,
		domain.IPv4AdditionsRuleTag,
		"proxy-domains-permanent",
		"proxy-ip-permanent",
		"user-default",
	}
	for index, payload := range payloads {
		if got := mixedTags(mixedRules(t, payload)); !reflect.DeepEqual(got, wantTags) {
			t.Fatalf("replayed payload %d rule order = %v; want %v", index, got, wantTags)
		}
		if !strings.Contains(string(payload), `"domain:keep.example"`) ||
			!strings.Contains(string(payload), `"192.0.2.30"`) {
			t.Fatalf("replayed payload %d lost mixed resources: %s", index, payload)
		}
	}

	setRuntimeFailure(client, errors.New("runtime unavailable"))
	identity.setToken("runtime-c")
	if changed, err := fresh.Reconcile(ctx); err == nil || changed {
		t.Fatalf("failed Reconcile() = %t, %v; want transient error without recorded token", changed, err)
	}
	setRuntimeFailure(client, nil)
	if changed, err := fresh.Reconcile(ctx); err != nil || !changed {
		t.Fatalf("retry Reconcile() = %t, %v; want mixed overlay applied", changed, err)
	}

	payloads, _ = client.recorded()
	if len(payloads) != 5 {
		t.Fatalf("runtime applies after transient failure and retry = %d; want five", len(payloads))
	}
	if !reflect.DeepEqual(payloads[2], payloads[3]) || !reflect.DeepEqual(payloads[3], payloads[4]) {
		t.Fatal("failed and retried mixed replays differ from the stable overlay")
	}
}
