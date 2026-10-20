package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCachedHelperMetadataRetainsPollingAddressDuringOutage(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cache := newTestMetadataCache(t)
	cached := validHelperMetadata(9, "127.0.0.1:11085")
	if err := cache.Save(cached); err != nil {
		t.Fatal(err)
	}
	source := &metadataSourceFake{errors: []error{ErrHelperUnavailable, ErrHelperUnavailable}}
	manager := newTestMetadataManager(t, source, cache)
	if err := manager.Initialize(context.Background(), now); err != nil {
		t.Fatal(err)
	}

	snapshot := manager.Snapshot()
	if !snapshot.Available || !snapshot.Stale || snapshot.Metadata.API != "127.0.0.1:11085" {
		t.Fatalf("startup snapshot = %#v", snapshot)
	}
	if _, err := manager.Refresh(context.Background(), now.Add(time.Second)); !errors.Is(err, ErrHelperUnavailable) {
		t.Fatalf("refresh error = %v", err)
	}
	after := manager.Snapshot()
	if !after.Available || !after.Stale || after.Metadata.API != cached.API {
		t.Fatalf("snapshot after outage = %#v", after)
	}
}

func TestMetadataAllowlistKeepsStatsLabelsBounded(t *testing.T) {
	snapshot := MetadataSnapshot{Available: true, Metadata: HelperMetadata{
		InboundTags: []string{"tproxy-in"}, OutboundTags: []string{"proxy", "direct"},
	}}
	tests := []struct {
		key  XrayStatKey
		want bool
	}{
		{key: XrayStatKey{Scope: "inbound", Tag: "tproxy-in"}, want: true},
		{key: XrayStatKey{Scope: "outbound", Tag: "proxy"}, want: true},
		{key: XrayStatKey{Scope: "outbound", Tag: "attacker-controlled"}, want: false},
		{key: XrayStatKey{Scope: "user", Tag: "alice"}, want: false},
	}
	for _, test := range tests {
		if got := metadataAllowsStat(snapshot, test.key); got != test.want {
			t.Errorf("metadataAllowsStat(%#v) = %v; want %v", test.key, got, test.want)
		}
	}
	if metadataAllowsStat(MetadataSnapshot{}, XrayStatKey{Scope: "outbound", Tag: "unknown"}) {
		t.Fatal("missing metadata admitted a Prometheus label")
	}
}
