package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		bad   bool
	}{
		{name: "lowercase and trailing dot", input: "EXAMPLE.COM.", want: "example.com"},
		{name: "trim whitespace", input: "  Sub.Example.COM  ", want: "sub.example.com"},
		{name: "subdomain", input: "sub.example.com", want: "sub.example.com"},
		{name: "single label", input: "localhost", bad: true},
		{name: "wildcard expression", input: "*.example.com", bad: true},
		{name: "domain routing expression", input: "domain:example.com", bad: true},
		{name: "geosite routing expression", input: "geosite:category", bad: true},
		{name: "regexp routing expression", input: "regexp:.*", bad: true},
		{name: "URL scheme", input: "https://example.com", bad: true},
		{name: "URL path", input: "example.com/path", bad: true},
		{name: "port", input: "example.com:443", bad: true},
		{name: "empty label", input: "example..com", bad: true},
		{name: "leading hyphen", input: "-bad.example.com", bad: true},
		{name: "trailing hyphen", input: "bad-.example.com", bad: true},
		{name: "underscore", input: "a_b.example.com", bad: true},
		{name: "non-ASCII label", input: "bücher.example", bad: true},
		{name: "multiple trailing dots", input: "example.com..", bad: true},
		{name: "label over 63 characters", input: strings.Repeat("a", 64) + ".example", bad: true},
		{name: "name over 253 characters", input: strings.Repeat("a.", 126) + "aa", bad: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := Normalize(testCase.input)
			if testCase.bad {
				if err == nil {
					t.Fatalf("Normalize(%q) succeeded with %q", testCase.input, got)
				}
				return
			}
			if err != nil || got != testCase.want {
				t.Fatalf("Normalize(%q) = %q, %v; want %q", testCase.input, got, err, testCase.want)
			}
		})
	}
}

func TestNormalizeListSortsAndDeduplicates(t *testing.T) {
	got, err := NormalizeList([]string{"B.example", "a.example.", "b.EXAMPLE", "  "})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.example", "b.example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeList() = %#v; want %#v", got, want)
	}
}

func TestBuildOverlay(t *testing.T) {
	tests := []struct {
		name     string
		desired  []string
		baseline []string
		want     Overlay
	}{
		{
			name: "empty",
			want: Overlay{Additions: []string{}, Exclusions: []string{}, Rules: []ManagedRule{}},
		},
		{
			name:     "addition",
			desired:  []string{"new.example", "base.example"},
			baseline: []string{"base.example"},
			want: Overlay{
				Additions:  []string{"new.example"},
				Exclusions: []string{},
				Rules: []ManagedRule{{
					Type: "field", Domain: []string{"domain:new.example"}, OutboundTag: "proxy", RuleTag: AdditionsRuleTag,
				}},
			},
		},
		{
			name:     "exclusion",
			desired:  []string{"kept.example"},
			baseline: []string{"removed.example", "kept.example"},
			want: Overlay{
				Additions:  []string{},
				Exclusions: []string{"removed.example"},
				Rules: []ManagedRule{{
					Type: "field", Domain: []string{"domain:removed.example"}, OutboundTag: "direct", RuleTag: ExclusionsRuleTag,
				}},
			},
		},
		{
			name:     "mixed sorted and ordered",
			desired:  []string{"new-b.example", "common.example", "new-a.example", "new-a.example"},
			baseline: []string{"removed-b.example", "common.example", "removed-a.example"},
			want: Overlay{
				Additions:  []string{"new-a.example", "new-b.example"},
				Exclusions: []string{"removed-a.example", "removed-b.example"},
				Rules: []ManagedRule{
					{Type: "field", Domain: []string{"domain:removed-a.example", "domain:removed-b.example"}, OutboundTag: "direct", RuleTag: ExclusionsRuleTag},
					{Type: "field", Domain: []string{"domain:new-a.example", "domain:new-b.example"}, OutboundTag: "proxy", RuleTag: AdditionsRuleTag},
				},
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := BuildOverlay(testCase.desired, testCase.baseline); !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("BuildOverlay() = %#v; want %#v", got, testCase.want)
			}
		})
	}
}
