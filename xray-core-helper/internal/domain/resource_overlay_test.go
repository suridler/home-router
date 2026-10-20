package domain

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestBuildResourceOverlayMixedDomainsAndIPv4(t *testing.T) {
	desired := []string{
		" New.Example. ",
		"new.example",
		"same.example",
		"192.0.2.1",
		"10.0.0.1",
		"10.0.0.1",
	}
	baseline := []string{
		"same.example",
		"old.example",
		"OLD.example.",
		"192.0.2.2",
		"10.0.0.1",
	}

	got, err := BuildResourceOverlay(desired, baseline)
	if err != nil {
		t.Fatal(err)
	}
	want := Overlay{
		Additions:  []string{"192.0.2.1", "new.example"},
		Exclusions: []string{"192.0.2.2", "old.example"},
		Rules: []ManagedRule{
			{
				Type:        "field",
				Domain:      []string{"domain:old.example"},
				OutboundTag: "direct",
				RuleTag:     ExclusionsRuleTag,
			},
			{
				Type:        "field",
				IP:          []string{"192.0.2.2"},
				OutboundTag: "direct",
				RuleTag:     IPv4ExclusionsRuleTag,
			},
			{
				Type:        "field",
				Domain:      []string{"domain:new.example"},
				OutboundTag: "proxy",
				RuleTag:     AdditionsRuleTag,
			},
			{
				Type:        "field",
				IP:          []string{"192.0.2.1"},
				OutboundTag: "proxy",
				RuleTag:     IPv4AdditionsRuleTag,
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildResourceOverlay() = %#v; want %#v", got, want)
	}

	for _, rule := range got.Rules {
		for _, condition := range rule.Domain {
			name := strings.TrimPrefix(condition, "domain:")
			if _, err := netip.ParseAddr(name); err == nil {
				t.Fatalf("domain condition contains IPv4 address %q", condition)
			}
		}
	}
}

func TestBuildResourceOverlayStableOrderingIndependentOfInputOrder(t *testing.T) {
	desiredA := []string{"b.example", "A.example.", "192.0.2.2", "192.0.2.1", "b.EXAMPLE"}
	desiredB := []string{"192.0.2.1", "b.example", "A.EXAMPLE.", "192.0.2.2", "b.example"}
	baselineA := []string{"c.example", "192.0.2.3"}
	baselineB := []string{"192.0.2.3", "C.EXAMPLE."}

	first, err := BuildResourceOverlay(desiredA, baselineA)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildResourceOverlay(desiredB, baselineB)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("overlay depends on input order:\nfirst = %#v\nsecond = %#v", first, second)
	}

	want := Overlay{
		Additions:  []string{"192.0.2.1", "192.0.2.2", "a.example", "b.example"},
		Exclusions: []string{"192.0.2.3", "c.example"},
		Rules: []ManagedRule{
			{Type: "field", Domain: []string{"domain:c.example"}, OutboundTag: "direct", RuleTag: ExclusionsRuleTag},
			{Type: "field", IP: []string{"192.0.2.3"}, OutboundTag: "direct", RuleTag: IPv4ExclusionsRuleTag},
			{Type: "field", Domain: []string{"domain:a.example", "domain:b.example"}, OutboundTag: "proxy", RuleTag: AdditionsRuleTag},
			{Type: "field", IP: []string{"192.0.2.1", "192.0.2.2"}, OutboundTag: "proxy", RuleTag: IPv4AdditionsRuleTag},
		},
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("BuildResourceOverlay() = %#v; want %#v", first, want)
	}
}

func TestBuildResourceOverlayMatchesBuildOverlayForDomains(t *testing.T) {
	tests := []struct {
		name     string
		desired  []string
		baseline []string
	}{
		{name: "empty", desired: []string{}, baseline: []string{}},
		{name: "addition", desired: []string{"a.example", "b.example", "c.example"}, baseline: []string{"b.example"}},
		{name: "exclusion", desired: []string{"b.example"}, baseline: []string{"a.example", "b.example", "c.example"}},
		{name: "mixed", desired: []string{"b.example", "d.example"}, baseline: []string{"a.example", "b.example", "c.example"}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			want := BuildOverlay(testCase.desired, testCase.baseline)
			got, err := BuildResourceOverlay(testCase.desired, testCase.baseline)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("BuildResourceOverlay() = %#v; want BuildOverlay() = %#v", got, want)
			}
		})
	}
}

func TestBuildResourceOverlayEmptyIsNonNil(t *testing.T) {
	tests := []struct {
		name     string
		desired  []string
		baseline []string
	}{
		{name: "nil", desired: nil, baseline: nil},
		{name: "empty", desired: []string{}, baseline: []string{}},
		{name: "blank", desired: []string{" ", "\t\n"}, baseline: []string{""}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := BuildResourceOverlay(testCase.desired, testCase.baseline)
			if err != nil {
				t.Fatal(err)
			}
			if got.Additions == nil {
				t.Fatal("Additions is nil; want non-nil empty slice")
			}
			if got.Exclusions == nil {
				t.Fatal("Exclusions is nil; want non-nil empty slice")
			}
			if got.Rules == nil {
				t.Fatal("Rules is nil; want non-nil empty slice")
			}
			if len(got.Additions) != 0 || len(got.Exclusions) != 0 || len(got.Rules) != 0 {
				t.Fatalf("BuildResourceOverlay() = %#v; want empty overlay", got)
			}
		})
	}
}

func TestBuildResourceOverlayRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		desired  []string
		baseline []string
		invalid  string
	}{
		{
			name:     "invalid desired",
			desired:  []string{"valid.example", "bad..example"},
			baseline: []string{"valid.example"},
			invalid:  "bad..example",
		},
		{
			name:     "invalid baseline",
			desired:  []string{"valid.example", "192.0.2.1"},
			baseline: []string{"192.0.2.0/24"},
			invalid:  "192.0.2.0/24",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := BuildResourceOverlay(testCase.desired, testCase.baseline)
			if err == nil {
				t.Fatalf("BuildResourceOverlay() succeeded with %#v", got)
			}
			if !reflect.DeepEqual(got, Overlay{}) {
				t.Fatalf("BuildResourceOverlay() = %#v; want zero Overlay on error", got)
			}
			if strings.Contains(err.Error(), testCase.invalid) {
				t.Fatalf("error leaks raw input %q: %v", testCase.invalid, err)
			}
		})
	}
}

func TestBuildResourceOverlayIPOnlyRuleJSON(t *testing.T) {
	overlay, err := BuildResourceOverlay([]string{"192.0.2.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(overlay.Rules) != 1 {
		t.Fatalf("Rules = %#v; want one IP additions rule", overlay.Rules)
	}

	data, err := json.Marshal(overlay.Rules[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, exists := decoded["domain"]; exists {
		t.Fatalf("IP-only rule JSON includes domain: %s", data)
	}
	ip, exists := decoded["ip"]
	if !exists {
		t.Fatalf("IP-only rule JSON missing ip: %s", data)
	}
	wantIP, ok := ip.([]any)
	if !ok || len(wantIP) != 1 || wantIP[0] != "192.0.2.1" {
		t.Fatalf("IP-only rule JSON ip = %#v; want [\"192.0.2.1\"]", ip)
	}
}
