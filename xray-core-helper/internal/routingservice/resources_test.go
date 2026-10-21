package routingservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/domain"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type resourceRunner struct {
	rules                              map[string]domain.ManagedRule
	appended                           [][]domain.ManagedRule
	removed                            []string
	failAppend, lostAppend, failRemove bool
}

func (r *resourceRunner) Run(_ context.Context, _ string, args []string) ([]byte, []byte, error) {
	switch args[1] {
	case "lsrules":
		items := make([]domain.ManagedRule, 0, len(r.rules))
		for _, rule := range r.rules {
			items = append(items, rule)
		}
		raw, _ := json.Marshal(struct {
			Rules []domain.ManagedRule `json:"rules"`
		}{items})
		return raw, nil, nil
	case "adrules":
		if !strings.Contains(strings.Join(args, " "), "--append") {
			return nil, nil, errors.New("full replace forbidden")
		}
		raw, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			return nil, nil, err
		}
		var payload struct {
			Routing struct {
				Rules []domain.ManagedRule `json:"rules"`
			} `json:"routing"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, nil, err
		}
		r.appended = append(r.appended, payload.Routing.Rules)
		if r.failAppend {
			return nil, nil, errors.New("append unavailable")
		}
		for _, rule := range payload.Routing.Rules {
			if _, exists := r.rules[rule.RuleTag]; exists {
				return nil, nil, errors.New("duplicate tag")
			}
			r.rules[rule.RuleTag] = rule
		}
		if r.lostAppend {
			return nil, nil, errors.New("response lost")
		}
	case "rmrules":
		tag := args[len(args)-1]
		r.removed = append(r.removed, tag)
		if r.failRemove {
			return nil, nil, errors.New("remove unavailable")
		}
		delete(r.rules, tag)
	}
	return []byte(`{}`), nil, nil
}
func resourcePayload(t *testing.T, domains, ips []string) xrayconfig.RoutingPayload {
	t.Helper()
	overlay, err := domain.BuildResourceOverlay(append(append([]string{}, domains...), ips...), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The configuration projection includes static references. None may reach adrules.
	rules := []domain.ManagedRule{{Type: "field", RuleTag: "static", Domain: []string{"geosite:unreadable"}, OutboundTag: "direct"}}
	rules = append(rules, overlay.Rules...)
	raw, err := json.Marshal(struct {
		Rules []domain.ManagedRule `json:"rules"`
	}{rules})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func newResourceRunner() *resourceRunner {
	return &resourceRunner{rules: map[string]domain.ManagedRule{
		"static":                        {RuleTag: "static", OutboundTag: "direct"},
		resourceRulePrefix + "operator": {RuleTag: resourceRulePrefix + "operator", OutboundTag: "direct"},
	}}
}
func TestResourcesAppendOnlyMissingRemoveOnlyObsoleteAndReplayAfterRestart(t *testing.T) {
	r := newResourceRunner()
	client := newTestClient(t, r)
	ctx := context.Background()
	first := resourcePayload(t, []string{"one.example"}, []string{"192.0.2.1"})
	if err := client.ApplyRouting(ctx, first); err != nil {
		t.Fatal(err)
	}
	if len(r.appended) != 1 || len(r.appended[0]) != 2 {
		t.Fatal("want one append of two resources")
	}
	for _, rule := range r.appended[0] {
		if len(rule.Domain)+len(rule.IP) != 1 || !ownedResourceTag(rule.RuleTag) {
			t.Fatalf("not a resource rule: %+v", rule)
		}
	}
	if err := client.ApplyRouting(ctx, first); err != nil {
		t.Fatal(err)
	}
	if len(r.appended) != 1 || len(r.removed) != 0 {
		t.Fatal("unchanged apply mutated runtime")
	}
	oldRules := r.appended[0]
	second := resourcePayload(t, []string{"one.example", "two.example"}, []string{"192.0.2.1"})
	if err := client.ApplyRouting(ctx, second); err != nil {
		t.Fatal(err)
	}
	if len(r.appended) != 2 || len(r.appended[1]) != 1 || len(r.removed) != 0 {
		t.Fatal("adding one resource recreated others")
	}
	for _, rule := range oldRules {
		if !reflect.DeepEqual(r.rules[rule.RuleTag], rule) {
			t.Fatal("existing resource changed")
		}
	}
	third := resourcePayload(t, []string{"two.example"}, []string{"192.0.2.1"})
	if err := client.ApplyRouting(ctx, third); err != nil {
		t.Fatal(err)
	}
	if len(r.appended) != 2 || len(r.removed) != 1 {
		t.Fatal("delete must remove exactly one rule")
	}
	// A helper restart has no cached routing state; listing tags is sufficient.
	client = newTestClient(t, r)
	if err := client.ApplyRouting(ctx, third); err != nil {
		t.Fatal(err)
	}
	if len(r.appended) != 2 || len(r.removed) != 1 {
		t.Fatal("helper restart duplicated rules")
	}
	// Xray restart discards runtime resource rules.
	r.rules = newResourceRunner().rules
	if err := client.ApplyRouting(ctx, third); err != nil {
		t.Fatal(err)
	}
	if len(r.appended) != 3 || len(r.appended[2]) != 2 {
		t.Fatal("Xray restart did not restore resource rules")
	}
	if err := client.ApplyRouting(ctx, resourcePayload(t, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if len(r.rules) != 2 || r.rules["static"].RuleTag != "static" {
		t.Fatal("static or foreign rules were changed")
	}
}
func TestAppendFailureAndLostResponseConvergeWithoutBlindRetry(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "response-lost"}[lost], func(t *testing.T) {
			r := newResourceRunner()
			r.failAppend = !lost
			r.lostAppend = lost
			client := newTestClient(t, r)
			payload := resourcePayload(t, []string{"one.example"}, nil)
			err := client.ApplyRouting(context.Background(), payload)
			if (err == nil) != lost {
				t.Fatalf("unexpected append result: %v", err)
			}
			if len(r.appended) != 1 {
				t.Fatal("append was blindly retried")
			}
			r.failAppend = false
			r.lostAppend = false
			if err := client.ApplyRouting(context.Background(), payload); err != nil {
				t.Fatal(err)
			}
			want := 2
			if lost {
				want = 1
			}
			if len(r.appended) != want {
				t.Fatal("retry duplicated successful append")
			}
		})
	}
}
func TestResourceRemovalFailureRecoversAndClearsOwnedGroupTags(t *testing.T) {
	r := newResourceRunner()
	client := newTestClient(t, r)
	ctx := context.Background()
	payload := resourcePayload(t, []string{"one.example"}, nil)
	if err := client.ApplyRouting(ctx, payload); err != nil {
		t.Fatal(err)
	}
	r.rules[domain.AdditionsRuleTag] = domain.ManagedRule{RuleTag: domain.AdditionsRuleTag}
	r.rules[domain.ExclusionsRuleTag] = domain.ManagedRule{RuleTag: domain.ExclusionsRuleTag}
	r.failRemove = true
	if err := client.ApplyRouting(ctx, payload); err == nil {
		t.Fatal("remove failure hidden")
	}
	r.failRemove = false
	if err := client.ApplyRouting(ctx, payload); err != nil {
		t.Fatal(err)
	}
	if len(r.appended) != 1 {
		t.Fatal("remove retry recreated current resource")
	}
	for _, tag := range []string{domain.AdditionsRuleTag, domain.ExclusionsRuleTag} {
		if _, ok := r.rules[tag]; ok {
			t.Fatal("obsolete owned group retained")
		}
	}
}
func TestMalformedListDoesNotDeleteOrAppend(t *testing.T) {
	r := &fakeRunner{stdout: []byte(`{}`)}
	client := newTestClient(t, r)
	if err := client.ApplyRouting(context.Background(), resourcePayload(t, []string{"one.example"}, nil)); err == nil {
		t.Fatal("malformed list accepted")
	}
	if len(r.calls) != 1 || r.calls[0].args[1] != "lsrules" {
		t.Fatal("mutated after invalid list")
	}
}
func TestResourceTagsAreStableAndTypeSpecific(t *testing.T) {
	a, err := resourceRules(resourcePayload(t, []string{"one.example", "two.example"}, []string{"192.0.2.1"}))
	if err != nil {
		t.Fatal(err)
	}
	b, err := resourceRules(resourcePayload(t, []string{"two.example", "one.example"}, []string{"192.0.2.1"}))
	if err != nil {
		t.Fatal(err)
	}
	tags := func(rules []domain.ManagedRule) []string {
		out := []string{}
		for _, r := range rules {
			out = append(out, r.RuleTag)
		}
		sort.Strings(out)
		return out
	}
	if !reflect.DeepEqual(tags(a), tags(b)) {
		t.Fatal("tags depend on input order")
	}
}
