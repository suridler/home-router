package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestReadinessRequiresInitialMetadataButAcceptsStaleCache(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "routing.db"), 24*time.Hour, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	metadata := &MetadataManager{}
	mux := http.NewServeMux()
	registerRoutes(mux, nil, store, nil, metadata, nil, nil)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing metadata status = %d, body = %s", response.Code, response.Body.String())
	}
	var unavailable map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &unavailable); err != nil {
		t.Fatal(err)
	}
	if unavailable["code"] != "metadata_unavailable" {
		t.Fatalf("missing metadata response = %#v", unavailable)
	}

	metadata.metadata = validHelperMetadata(3, "127.0.0.1:10085")
	metadata.available = true
	metadata.stale = true
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("cached metadata status = %d, body = %s", response.Code, response.Body.String())
	}
	var ready map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &ready); err != nil {
		t.Fatal(err)
	}
	if ready["status"] != "ready" || ready["metadata"] != "stale" {
		t.Fatalf("cached metadata response = %#v", ready)
	}
}

func TestMissingMetadataPreservesRestoredTelemetrySnapshot(t *testing.T) {
	collector := &XrayStatsCollector{
		metrics: &Metrics{
			XrayAPIUp:       prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_metadata_api_up"}),
			XrayQueryErrors: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_metadata_query_errors"}),
		},
		snapshot: XrayStatsSnapshot{
			Up: true, Uptime: 120, StartUnix: 1000, RestartCount: 2,
			Counters: []XrayCounterSnapshot{{Key: XrayStatKey{Name: "outbound>>>proxy>>>traffic>>>uplink"}, CumulativeBytes: 42}},
		},
	}
	collector.recordMetadataUnavailable()
	snapshot := collector.Snapshot()
	if snapshot.Up || snapshot.Error != ErrMetadataUnavailable.Error() || snapshot.QueryErrors != 1 {
		t.Fatalf("unavailable snapshot state = %#v", snapshot)
	}
	if snapshot.Uptime != 120 || snapshot.StartUnix != 1000 || snapshot.RestartCount != 2 || len(snapshot.Counters) != 1 || snapshot.Counters[0].CumulativeBytes != 42 {
		t.Fatalf("stored telemetry was damaged: %#v", snapshot)
	}
}
