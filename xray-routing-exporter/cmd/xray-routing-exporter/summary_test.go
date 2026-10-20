package main

import "testing"

func TestDestinationHost(t *testing.T) {
	tests := map[string]string{
		"example.com:443":   "example.com",
		"149.154.167.41:80": "149.154.167.41",
		"[2001:db8::1]:443": "2001:db8::1",
		"2001:db8::1":       "2001:db8::1",
		"example.com":       "example.com",
		"":                  "",
	}

	for input, want := range tests {
		if got := destinationHost(input); got != want {
			t.Fatalf("destinationHost(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSummaryKeyUsesDestinationHost(t *testing.T) {
	a := ReportRow{ClientIP: "192.0.2.51", Destination: "91.105.192.100:80", Outbound: "proxy"}
	b := ReportRow{ClientIP: "192.0.2.51", Destination: "91.105.192.100:443", Outbound: "proxy"}
	if summaryKey(a) != summaryKey(b) {
		t.Fatalf("summary keys differ for same destination with different ports")
	}
}
