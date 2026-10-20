package routerdiagnostics

import (
	"bytes"
	"testing"
)

func TestObservedRouteAndServiceOutput(t *testing.T) {
	for _, tt := range []struct {
		raw     string
		want    bool
		invalid bool
	}{
		{"default via 192.0.2.1 dev eth0\n", true, false},
		{"default dev eth0\n", true, false},
		{"192.0.2.0/24 dev eth0\n", false, false},
		{"default\n", false, true},
	} {
		got, err := hasDefaultRoute([]byte(tt.raw))
		if got != tt.want || (err != nil) != tt.invalid {
			t.Fatal("wrong route observation")
		}
	}
	for _, tt := range []struct {
		raw     string
		want    bool
		invalid bool
	}{{"active\n", true, false}, {"inactive\n", false, false}, {"nonsense", false, true}} {
		got, err := activeState([]byte(tt.raw))
		if got != tt.want || (err != nil) != tt.invalid {
			t.Fatal("wrong service observation")
		}
	}
}
func TestOutputBounds(t *testing.T) {
	for _, raw := range [][]byte{bytes.Repeat([]byte("x"), maxDiagnosticLine+1), []byte{0xff}, []byte("active\x00")} {
		if _, err := boundedLines(raw); err == nil {
			t.Fatal("invalid output accepted")
		}
	}
}
