package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type metadataSourceFake struct {
	responses []HelperMetadata
	errors    []error
	calls     int
}

func (fake *metadataSourceFake) FetchMetadata(context.Context) (HelperMetadata, error) {
	index := fake.calls
	fake.calls++
	if index < len(fake.errors) && fake.errors[index] != nil {
		return HelperMetadata{}, fake.errors[index]
	}
	if index >= len(fake.responses) {
		return HelperMetadata{}, errors.New("unexpected metadata fetch")
	}
	return cloneHelperMetadata(fake.responses[index]), nil
}

func TestMetadataManagerColdStartAndGenerationRefresh(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	first := validHelperMetadata(1, "127.0.0.1:10085")
	second := validHelperMetadata(2, "127.0.0.1:10086")
	source := &metadataSourceFake{responses: []HelperMetadata{first, second}}
	cache := newTestMetadataCache(t)
	manager := newTestMetadataManager(t, source, cache)

	if err := manager.Initialize(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Snapshot()
	if !snapshot.Available || snapshot.Stale || snapshot.Metadata.Generation != 1 || !snapshot.NextAttempt.Equal(now.Add(time.Minute)) {
		t.Fatalf("cold snapshot = %#v", snapshot)
	}
	if changed, err := manager.Refresh(context.Background(), now.Add(59*time.Second)); err != nil || changed || source.calls != 1 {
		t.Fatalf("early refresh = %v, %v; calls = %d", changed, err, source.calls)
	}
	changed, err := manager.Refresh(context.Background(), now.Add(time.Minute))
	if err != nil || !changed {
		t.Fatalf("generation refresh = %v, %v", changed, err)
	}
	snapshot = manager.Snapshot()
	if snapshot.Metadata.Generation != 2 || snapshot.Metadata.API != "127.0.0.1:10086" || snapshot.Stale {
		t.Fatalf("refreshed snapshot = %#v", snapshot)
	}
	loaded, err := cache.Load()
	if err != nil || loaded.Generation != 2 {
		t.Fatalf("persisted metadata = %#v, %v", loaded, err)
	}
}

func TestMetadataManagerUsesCacheDuringHelperOutageAndBacksOff(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cache := newTestMetadataCache(t)
	if err := cache.Save(validHelperMetadata(4, "127.0.0.1:10085")); err != nil {
		t.Fatal(err)
	}
	source := &metadataSourceFake{errors: []error{ErrHelperUnavailable, ErrHelperUnavailable, ErrHelperUnavailable}}
	manager := newTestMetadataManager(t, source, cache)
	if err := manager.Initialize(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Snapshot()
	if !snapshot.Available || !snapshot.Stale || snapshot.Metadata.Generation != 4 || !snapshot.NextAttempt.Equal(now.Add(time.Second)) {
		t.Fatalf("cached snapshot = %#v", snapshot)
	}
	if changed, err := manager.Refresh(context.Background(), now.Add(500*time.Millisecond)); err != nil || changed || source.calls != 1 {
		t.Fatalf("refresh inside backoff = %v, %v; calls = %d", changed, err, source.calls)
	}
	if _, err := manager.Refresh(context.Background(), now.Add(time.Second)); !errors.Is(err, ErrHelperUnavailable) {
		t.Fatalf("first retry error = %v", err)
	}
	if next := manager.Snapshot().NextAttempt; !next.Equal(now.Add(3 * time.Second)) {
		t.Fatalf("second backoff deadline = %s", next)
	}
	if _, err := manager.Refresh(context.Background(), now.Add(3*time.Second)); !errors.Is(err, ErrHelperUnavailable) {
		t.Fatalf("second retry error = %v", err)
	}
	if next := manager.Snapshot().NextAttempt; !next.Equal(now.Add(7 * time.Second)) {
		t.Fatalf("third backoff deadline = %s", next)
	}
}

func TestMetadataManagerRejectsCorruptCacheWhenHelperIsUnavailable(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cache := newTestMetadataCache(t)
	if err := os.WriteFile(cache.path, []byte(`{"api":"secret","outboundSettings":{"password":"secret"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	manager := newTestMetadataManager(t, &metadataSourceFake{errors: []error{ErrHelperUnavailable}}, cache)
	if err := manager.Initialize(context.Background(), now); !errors.Is(err, ErrMetadataCacheCorrupt) {
		t.Fatalf("initialize error = %v", err)
	}
	if manager.Snapshot().Available {
		t.Fatal("corrupt cache became available")
	}
}

func TestFileMetadataCacheProtectsAndStrictlyLoadsSnapshot(t *testing.T) {
	cache := newTestMetadataCache(t)
	metadata := validHelperMetadata(8, "127.0.0.1:10085")
	if err := cache.Save(metadata); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cache.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("cache mode = %o", info.Mode().Perm())
	}
	loaded, err := cache.Load()
	if err != nil || loaded.Generation != metadata.Generation || loaded.ContentHash != metadata.ContentHash {
		t.Fatalf("loaded = %#v, %v", loaded, err)
	}
}

func newTestMetadataCache(t *testing.T) *FileMetadataCache {
	t.Helper()
	cache, err := NewFileMetadataCache(filepath.Join(t.TempDir(), "helper-metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func newTestMetadataManager(t *testing.T, source MetadataSource, cache MetadataCache) *MetadataManager {
	t.Helper()
	manager, err := NewMetadataManager(source, cache, time.Minute, time.Second, 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func validHelperMetadata(generation uint64, api string) HelperMetadata {
	return HelperMetadata{
		API: api, InboundTags: []string{"tproxy-in"}, OutboundTags: []string{"proxy", "direct"},
		Generation: generation, ContentHash: strings.Repeat("a", 64),
		ModifiedAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
	}
}
