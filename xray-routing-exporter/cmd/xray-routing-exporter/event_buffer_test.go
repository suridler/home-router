package main

import (
	"testing"
	"time"
)

func TestEventBufferQuery(t *testing.T) {
	b := NewEventBuffer(3)
	base := time.Date(2026, 9, 23, 20, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		tm := base.Add(time.Duration(i) * time.Minute)
		raw := RoutingEvent{Source: "192.0.2.51:1234", Destination: "example.com:443", OutboundTag: "proxy", TS: tm.Unix()}
		n := NormalizedEvent{Time: tm, ClientIP: "192.0.2.51", ClientPort: 1234}
		b.Add(raw, n, tm)
	}

	events, oldest, newest, size := b.Query("", "", 0, false, 100)
	if size != 3 {
		t.Fatalf("size=%d, want 3", size)
	}
	if len(events) != 3 {
		t.Fatalf("len(events)=%d, want 3", len(events))
	}
	if !oldest.Equal(base.Add(1*time.Minute)) || !newest.Equal(base.Add(3*time.Minute)) {
		t.Fatalf("unexpected bounds: oldest=%v newest=%v", oldest, newest)
	}

	cut := base.Add(2 * time.Minute)
	events, _, _, _ = b.Query("", "proxy", cut.Unix(), true, 100)
	if len(events) != 1 || !events[0].DateTime.Equal(base.Add(3*time.Minute)) {
		t.Fatalf("unexpected cutoff result: %+v", events)
	}
}

func TestEventBufferFiltersOutboundTag(t *testing.T) {
	b := NewEventBuffer(10)
	base := time.Date(2026, 9, 23, 20, 0, 0, 0, time.UTC)
	items := []struct {
		tm       time.Time
		outbound string
	}{
		{base, "proxy"},
		{base.Add(time.Second), "block"},
		{base.Add(2 * time.Second), "direct"},
	}
	for _, item := range items {
		raw := RoutingEvent{Source: "192.0.2.51:1234", Destination: "example.com:443", OutboundTag: item.outbound, TS: item.tm.Unix()}
		n := NormalizedEvent{Time: item.tm, ClientIP: "192.0.2.51", ClientPort: 1234}
		b.Add(raw, n, item.tm)
	}

	events, _, _, _ := b.Query("", "block", 0, false, 100)
	if len(events) != 1 || events[0].OutboundTag != "block" {
		t.Fatalf("unexpected outbound filter result: %+v", events)
	}
}

func TestParseEventTimestamp(t *testing.T) {
	ts, ok, err := parseEventTimestamp("1790177580")
	if err != nil || !ok || ts != 1790177580 {
		t.Fatalf("unexpected timestamp parse: ts=%d ok=%v err=%v", ts, ok, err)
	}
	_, ok, err = parseEventTimestamp("")
	if err != nil || ok {
		t.Fatalf("empty timestamp should be absent: ok=%v err=%v", ok, err)
	}
	_, _, err = parseEventTimestamp("23.09.2026 20:33")
	if err == nil {
		t.Fatal("expected human-readable timestamp to be rejected")
	}
}
