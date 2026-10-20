package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeResource(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		kind  ResourceKind
		bad   bool
	}{
		{name: "domain lowercase and trailing dot", input: "  EXAMPLE.COM.  ", want: "example.com", kind: DomainResource},
		{name: "subdomain", input: "Sub.Example.COM", want: "sub.example.com", kind: DomainResource},
		{name: "numeric label domain", input: "123.example", want: "123.example", kind: DomainResource},
		{name: "numeric labels in domain", input: "192.0.2.example", want: "192.0.2.example", kind: DomainResource},
		{name: "mixed numeric labels in domain", input: "1.2.3.example", want: "1.2.3.example", kind: DomainResource},

		{name: "ipv4", input: "192.0.2.1", want: "192.0.2.1", kind: IPv4Resource},
		{name: "private ipv4", input: "10.0.0.1", want: "10.0.0.1", kind: IPv4Resource},
		{name: "zero ipv4", input: "0.0.0.0", want: "0.0.0.0", kind: IPv4Resource},
		{name: "loopback ipv4", input: "127.0.0.1", want: "127.0.0.1", kind: IPv4Resource},
		{name: "broadcast ipv4", input: "255.255.255.255", want: "255.255.255.255", kind: IPv4Resource},
		{name: "ipv4 with whitespace", input: "  198.51.100.7 \t", want: "198.51.100.7", kind: IPv4Resource},

		{name: "malformed ipv4 out of range", input: "999.0.0.1", bad: true},
		{name: "malformed ipv4 leading zero", input: "192.168.001.1", bad: true},
		{name: "malformed ipv4 trailing dot", input: "192.0.2.1.", bad: true},
		{name: "all numeric domain", input: "1.2.3", bad: true},
		{name: "all numeric five labels", input: "1.2.3.4.5", bad: true},
		{name: "single label integer", input: "123", bad: true},
		{name: "single label name", input: "localhost", bad: true},
		{name: "ipv6", input: "2001:db8::1", bad: true},
		{name: "ipv6 loopback", input: "::1", bad: true},
		{name: "ipv4 mapped ipv6", input: "::ffff:192.0.2.1", bad: true},
		{name: "ipv4 mapped ipv6 uppercase", input: "::FFFF:192.0.2.1", bad: true},
		{name: "cidr", input: "192.0.2.0/24", bad: true},
		{name: "address with port", input: "192.0.2.1:443", bad: true},
		{name: "domain with port", input: "example.com:443", bad: true},
		{name: "url", input: "https://example.com", bad: true},
		{name: "url path", input: "example.com/path", bad: true},
		{name: "wildcard expression", input: "*.example.com", bad: true},
		{name: "domain routing expression", input: "domain:example.com", bad: true},
		{name: "geosite routing expression", input: "geosite:category", bad: true},
		{name: "regexp routing expression", input: "regexp:.*", bad: true},
		{name: "empty", input: "", bad: true},
		{name: "whitespace only", input: "   \t ", bad: true},
		{name: "empty label", input: "example..com", bad: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, kind, err := NormalizeResource(testCase.input)
			if testCase.bad {
				if err == nil {
					t.Fatalf("NormalizeResource(%q) succeeded with %q, %q", testCase.input, got, kind)
				}
				if got != "" || kind != "" {
					t.Fatalf("NormalizeResource(%q) returned partial result %q, %q with error", testCase.input, got, kind)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeResource(%q) error: %v", testCase.input, err)
			}
			if got != testCase.want || kind != testCase.kind {
				t.Fatalf("NormalizeResource(%q) = %q, %q; want %q, %q", testCase.input, got, kind, testCase.want, testCase.kind)
			}
		})
	}
}

func TestSplitResourcesSortsDeduplicatesAndSkipsBlank(t *testing.T) {
	domains, ipv4, err := SplitResources([]string{
		"B.example",
		"192.0.2.1",
		"  ",
		"a.example.",
		"192.0.2.1",
		"0.0.0.0",
		"A.EXAMPLE",
		"123.example",
		"\t\n",
	})
	if err != nil {
		t.Fatal(err)
	}

	wantDomains := []string{"123.example", "a.example", "b.example"}
	wantIPv4 := []string{"0.0.0.0", "192.0.2.1"}
	if !reflect.DeepEqual(domains, wantDomains) {
		t.Fatalf("domains = %#v; want %#v", domains, wantDomains)
	}
	if !reflect.DeepEqual(ipv4, wantIPv4) {
		t.Fatalf("ipv4 = %#v; want %#v", ipv4, wantIPv4)
	}
}

func TestSplitResourcesEmptyResultIsNonNil(t *testing.T) {
	tests := []struct {
		name   string
		values []string
	}{
		{name: "nil input", values: nil},
		{name: "empty input", values: []string{}},
		{name: "blank entries", values: []string{"", " ", "\t\n"}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			domains, ipv4, err := SplitResources(testCase.values)
			if err != nil {
				t.Fatal(err)
			}
			if domains == nil {
				t.Fatal("domains is nil; want non-nil empty slice")
			}
			if ipv4 == nil {
				t.Fatal("ipv4 is nil; want non-nil empty slice")
			}
			if len(domains) != 0 || len(ipv4) != 0 {
				t.Fatalf("domains, ipv4 = %#v, %#v; want both empty", domains, ipv4)
			}
		})
	}
}

func TestSplitResourcesInvalidReturnsNoPartialResults(t *testing.T) {
	invalidValues := []string{
		"999.0.0.1",
		"192.168.001.1",
		"192.0.2.1.",
		"::1",
		"::ffff:192.0.2.1",
		"192.0.2.0/24",
		"192.0.2.1:443",
		"https://example.com",
		"*.example.com",
	}

	for _, invalid := range invalidValues {
		domains, ipv4, err := SplitResources([]string{"a.example", invalid, "192.0.2.1"})
		if err == nil {
			t.Fatalf("SplitResources with %q succeeded", invalid)
		}
		if domains != nil || ipv4 != nil {
			t.Fatalf("SplitResources with %q returned partial results %#v, %#v", invalid, domains, ipv4)
		}
		if strings.Contains(err.Error(), invalid) {
			t.Fatalf("error leaks raw input %q: %v", invalid, err)
		}
	}
}
