package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
)

var (
	ErrMetadataUnavailable  = errors.New("helper metadata is unavailable")
	ErrMetadataCacheCorrupt = errors.New("helper metadata cache is corrupt")
)

type MetadataSource interface {
	FetchMetadata(context.Context) (HelperMetadata, error)
}

type MetadataCache interface {
	Load() (HelperMetadata, error)
	Save(HelperMetadata) error
}

type FileMetadataCache struct {
	path string
}

func NewFileMetadataCache(path string) (*FileMetadataCache, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("metadata cache path must be absolute")
	}
	return &FileMetadataCache{path: path}, nil
}

func (cache *FileMetadataCache) Load() (HelperMetadata, error) {
	file, err := os.Open(cache.path)
	if err != nil {
		return HelperMetadata{}, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxHelperResponseBody+1))
	if err != nil || len(body) > maxHelperResponseBody {
		return HelperMetadata{}, ErrMetadataCacheCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var metadata HelperMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return HelperMetadata{}, ErrMetadataCacheCorrupt
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return HelperMetadata{}, ErrMetadataCacheCorrupt
	}
	if err := validateHelperMetadata(metadata); err != nil {
		return HelperMetadata{}, ErrMetadataCacheCorrupt
	}
	return metadata, nil
}

func (cache *FileMetadataCache) Save(metadata HelperMetadata) error {
	if err := validateHelperMetadata(metadata); err != nil {
		return fmt.Errorf("refuse invalid helper metadata: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cache.path), 0o750); err != nil {
		return fmt.Errorf("create metadata cache directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(cache.path), ".helper-metadata-*")
	if err != nil {
		return fmt.Errorf("create metadata cache: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect metadata cache: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(metadata); err != nil {
		temporary.Close()
		return fmt.Errorf("encode metadata cache: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync metadata cache: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close metadata cache: %w", err)
	}
	if err := os.Rename(temporaryPath, cache.path); err != nil {
		return fmt.Errorf("replace metadata cache: %w", err)
	}
	return nil
}

type MetadataManager struct {
	source          MetadataSource
	cache           MetadataCache
	refreshInterval time.Duration
	backoffMin      time.Duration
	backoffMax      time.Duration

	mu          sync.RWMutex
	metadata    HelperMetadata
	available   bool
	stale       bool
	failures    uint
	nextAttempt time.Time
}

type MetadataSnapshot struct {
	Metadata    HelperMetadata
	Available   bool
	Stale       bool
	NextAttempt time.Time
}

func NewMetadataManager(source MetadataSource, cache MetadataCache, refreshInterval, backoffMin, backoffMax time.Duration) (*MetadataManager, error) {
	if source == nil || cache == nil || refreshInterval <= 0 || backoffMin <= 0 || backoffMax < backoffMin {
		return nil, errors.New("invalid metadata manager settings")
	}
	return &MetadataManager{
		source: source, cache: cache, refreshInterval: refreshInterval,
		backoffMin: backoffMin, backoffMax: backoffMax,
	}, nil
}

func (manager *MetadataManager) Initialize(ctx context.Context, now time.Time) error {
	metadata, err := manager.source.FetchMetadata(ctx)
	if err == nil {
		return manager.accept(metadata, now)
	}
	cached, cacheErr := manager.cache.Load()
	if cacheErr != nil {
		manager.recordFailure(now)
		if errors.Is(cacheErr, ErrMetadataCacheCorrupt) {
			return ErrMetadataCacheCorrupt
		}
		return ErrMetadataUnavailable
	}
	manager.mu.Lock()
	manager.metadata = cached
	manager.available = true
	manager.stale = true
	manager.failures++
	manager.nextAttempt = now.Add(manager.backoffDelay(manager.failures))
	manager.mu.Unlock()
	return nil
}

func (manager *MetadataManager) Refresh(ctx context.Context, now time.Time) (bool, error) {
	manager.mu.RLock()
	due := manager.nextAttempt.IsZero() || !now.Before(manager.nextAttempt)
	manager.mu.RUnlock()
	if !due {
		return false, nil
	}
	metadata, err := manager.source.FetchMetadata(ctx)
	if err != nil {
		manager.recordFailure(now)
		return false, err
	}
	manager.mu.RLock()
	changed := !manager.available || !reflect.DeepEqual(manager.metadata, metadata)
	manager.mu.RUnlock()
	if err := manager.accept(metadata, now); err != nil {
		return false, err
	}
	return changed, nil
}

func (manager *MetadataManager) Snapshot() MetadataSnapshot {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return MetadataSnapshot{
		Metadata:    cloneHelperMetadata(manager.metadata),
		Available:   manager.available,
		Stale:       manager.stale,
		NextAttempt: manager.nextAttempt,
	}
}

func (manager *MetadataManager) accept(metadata HelperMetadata, now time.Time) error {
	if err := validateHelperMetadata(metadata); err != nil {
		manager.recordFailure(now)
		return ErrIncompatibleHelperResponse
	}
	manager.mu.RLock()
	changed := !manager.available || !reflect.DeepEqual(manager.metadata, metadata)
	manager.mu.RUnlock()
	if changed {
		if err := manager.cache.Save(metadata); err != nil {
			manager.recordFailure(now)
			return fmt.Errorf("save helper metadata cache: %w", err)
		}
	}
	manager.mu.Lock()
	manager.metadata = cloneHelperMetadata(metadata)
	manager.available = true
	manager.stale = false
	manager.failures = 0
	manager.nextAttempt = now.Add(manager.refreshInterval)
	manager.mu.Unlock()
	return nil
}

func (manager *MetadataManager) recordFailure(now time.Time) {
	manager.mu.Lock()
	manager.failures++
	manager.stale = manager.available
	manager.nextAttempt = now.Add(manager.backoffDelay(manager.failures))
	manager.mu.Unlock()
}

func (manager *MetadataManager) backoffDelay(failures uint) time.Duration {
	delay := manager.backoffMin
	for attempt := uint(1); attempt < failures && delay < manager.backoffMax; attempt++ {
		if delay > manager.backoffMax/2 {
			return manager.backoffMax
		}
		delay *= 2
	}
	if delay > manager.backoffMax {
		return manager.backoffMax
	}
	return delay
}

func cloneHelperMetadata(metadata HelperMetadata) HelperMetadata {
	metadata.InboundTags = append([]string(nil), metadata.InboundTags...)
	metadata.OutboundTags = append([]string(nil), metadata.OutboundTags...)
	return metadata
}
