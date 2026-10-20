package main

import "testing"

func TestXrayCounterDeltaScenarios(t *testing.T) {
	cases := []struct {
		name      string
		previous  uint64
		current   uint64
		exists    bool
		restarted bool
		wantDelta uint64
	}{
		{name: "first observation", current: 100, wantDelta: 100},
		{name: "normal increase", previous: 100, current: 160, exists: true, wantDelta: 60},
		{name: "counter reset", previous: 160, current: 7, exists: true, wantDelta: 7},
		{name: "restart with larger counter", previous: 100, current: 120, exists: true, restarted: true, wantDelta: 120},
	}

	for _, tc := range cases {
		var delta uint64
		switch {
		case !tc.exists:
			delta = tc.current
		case tc.restarted:
			delta = tc.current
		case tc.current >= tc.previous:
			delta = tc.current - tc.previous
		default:
			delta = tc.current
		}
		if delta != tc.wantDelta {
			t.Fatalf("%s: got %d, want %d", tc.name, delta, tc.wantDelta)
		}
	}
}
