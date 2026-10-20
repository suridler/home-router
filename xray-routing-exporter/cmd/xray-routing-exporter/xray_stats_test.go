package main

import "testing"

func TestParseXrayTrafficName(t *testing.T) {
	cases := []struct {
		name string
		want XrayStatKey
		ok   bool
	}{
		{
			name: "inbound>>>tproxy-in>>>traffic>>>uplink",
			want: XrayStatKey{Name: "inbound>>>tproxy-in>>>traffic>>>uplink", Scope: "inbound", Tag: "tproxy-in", Direction: "uplink"},
			ok:   true,
		},
		{
			name: "outbound>>>proxy>>>traffic>>>downlink",
			want: XrayStatKey{Name: "outbound>>>proxy>>>traffic>>>downlink", Scope: "outbound", Tag: "proxy", Direction: "downlink"},
			ok:   true,
		},
		{
			name: "outbound>>>proxy>>>online",
			ok:   false,
		},
		{
			name: "user>>>alice>>>traffic>>>uplink",
			ok:   false,
		},
	}

	for _, tc := range cases {
		got, ok := parseXrayTrafficName(tc.name)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("parseXrayTrafficName(%q) = %#v, %v; want %#v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
