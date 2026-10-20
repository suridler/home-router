package main

import "testing"

func TestXrayCounterDelta(t *testing.T) {
	tests := []struct {
		name      string
		previous  int64
		current   int64
		wantDelta uint64
		wantReset bool
	}{
		{name: "normal", previous: 100, current: 160, wantDelta: 60, wantReset: false},
		{name: "no change", previous: 100, current: 100, wantDelta: 0, wantReset: false},
		{name: "reset", previous: 100, current: 7, wantDelta: 7, wantReset: true},
		{name: "negative treated as zero", previous: 100, current: -1, wantDelta: 0, wantReset: true},
	}

	for _, tc := range tests {
		delta, reset := xrayCounterDelta(tc.previous, tc.current)
		if delta != tc.wantDelta || reset != tc.wantReset {
			t.Fatalf("%s: got delta=%d reset=%v, want delta=%d reset=%v", tc.name, delta, reset, tc.wantDelta, tc.wantReset)
		}
	}
}
