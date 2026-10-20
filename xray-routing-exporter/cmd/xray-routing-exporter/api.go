package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func registerRoutes(mux *http.ServeMux, aggregator *Aggregator, store *Store, metrics *Metrics, metadata *MetadataManager, xrayStats *XrayStatsCollector, eventBuffer *EventBuffer) {
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if err := store.db.Ping(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "error": err.Error()})
			return
		}
		metadataSnapshot := metadata.Snapshot()
		if !metadataSnapshot.Available {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "code": "metadata_unavailable"})
			return
		}
		metadataState := "fresh"
		if metadataSnapshot.Stale {
			metadataState = "stale"
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "metadata": metadataState})
	})

	mux.HandleFunc("/api/v1/stats/hour", func(w http.ResponseWriter, r *http.Request) {
		client := strings.TrimSpace(r.URL.Query().Get("client"))
		rows := aggregator.RecentHour(client)
		rows = applyLimit(rows, r.URL.Query().Get("limit"), 1000)
		writeJSON(w, http.StatusOK, map[string]any{
			"window":    "60m",
			"generated": time.Now().UTC(),
			"client":    client,
			"rows":      rows,
		})
	})

	mux.HandleFunc("/api/v1/stats/hour/summary", func(w http.ResponseWriter, r *http.Request) {
		client := strings.TrimSpace(r.URL.Query().Get("client"))
		rows := aggregator.RecentHourSummary(client)
		rows = applySummaryLimit(rows, r.URL.Query().Get("limit"), 1000)
		writeJSON(w, http.StatusOK, map[string]any{
			"window":    "60m",
			"generated": time.Now().UTC(),
			"client":    client,
			"rows":      rows,
		})
	})

	mux.HandleFunc("/api/v1/stats/30d", func(w http.ResponseWriter, r *http.Request) {
		_ = aggregator.flushPending()
		client := strings.TrimSpace(r.URL.Query().Get("client"))
		rows, err := store.Last30Days(client)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		rows = applyLimit(rows, r.URL.Query().Get("limit"), 5000)
		writeJSON(w, http.StatusOK, map[string]any{
			"window":    "30d",
			"generated": time.Now().UTC(),
			"client":    client,
			"rows":      rows,
		})
	})

	mux.HandleFunc("/api/v1/stats/30d/summary", func(w http.ResponseWriter, r *http.Request) {
		_ = aggregator.flushPending()
		client := strings.TrimSpace(r.URL.Query().Get("client"))
		rows, err := store.Last30DaysSummary(client)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		rows = applySummaryLimit(rows, r.URL.Query().Get("limit"), 5000)
		writeJSON(w, http.StatusOK, map[string]any{
			"window":    "30d",
			"generated": time.Now().UTC(),
			"client":    client,
			"rows":      rows,
		})
	})

	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		client := strings.TrimSpace(r.URL.Query().Get("client"))
		outboundTag := strings.TrimSpace(r.URL.Query().Get("outboundTag"))
		limit := parseEventLimit(r.URL.Query().Get("limit"), 100, 1000)

		timestampRaw := strings.TrimSpace(r.URL.Query().Get("timestamp"))
		timestamp, hasTimestamp, err := parseEventTimestamp(timestampRaw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		events, oldest, newest, bufferSize := eventBuffer.Query(client, outboundTag, timestamp, hasTimestamp, limit)
		response := map[string]any{
			"generated":       time.Now().UTC(),
			"client":          client,
			"outboundTag":     outboundTag,
			"timestamp":       timestampValue(timestamp, hasTimestamp),
			"count":           len(events),
			"limit":           limit,
			"buffer_size":     bufferSize,
			"buffer_capacity": eventBuffer.Capacity(),
			"oldest_event":    timeString(oldest),
			"newest_event":    timeString(newest),
			"events":          events,
		}
		writeJSON(w, http.StatusOK, response)
	})

	mux.HandleFunc("/api/v1/xray/stats", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, xrayStats.Snapshot())
	})

	_ = metrics // keeps the metrics registry dependency explicit for route setup.
}

func parseEventLimit(raw string, def, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func parseEventTimestamp(raw string) (int64, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, nil
	}
	raw = strings.Trim(raw, "\"'")
	ts, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ts < 0 {
		return 0, false, fmt.Errorf("invalid timestamp: use Unix timestamp in seconds")
	}
	return ts, true, nil
}

func timestampValue(ts int64, ok bool) any {
	if !ok {
		return nil
	}
	return ts
}

func timeString(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func applySummaryLimit(rows []SummaryRow, raw string, max int) []SummaryRow {
	limit := max
	if raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n < max {
			limit = n
		}
	}
	if len(rows) > limit {
		return rows[:limit]
	}
	return rows
}

func applyLimit(rows []ReportRow, raw string, max int) []ReportRow {
	limit := max
	if raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n < max {
			limit = n
		}
	}
	if len(rows) > limit {
		return rows[:limit]
	}
	return rows
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func requestLoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		_ = start
	})
}
