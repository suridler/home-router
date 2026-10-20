package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type WebhookHandler struct {
	aggregator *Aggregator
	events     *EventBuffer
	metrics    *Metrics
}

func NewWebhookHandler(aggregator *Aggregator, events *EventBuffer, metrics *Metrics) *WebhookHandler {
	return &WebhookHandler{aggregator: aggregator, events: events, metrics: metrics}
}

func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.metrics.WebhookRequests.WithLabelValues(r.Method, r.URL.Path).Inc()

	if r.Method != http.MethodPost {
		h.metrics.WebhookErrors.WithLabelValues("method").Inc()
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.URL.Path != "/webhook" && r.URL.Path != "/" {
		h.metrics.WebhookErrors.WithLabelValues("path").Inc()
		http.NotFound(w, r)
		return
	}

	body := http.MaxBytesReader(w, r.Body, 64*1024)
	defer body.Close()

	var event RoutingEvent
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&event); err != nil {
		h.metrics.WebhookErrors.WithLabelValues("json").Inc()
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if err := validateEvent(event); err != nil {
		h.metrics.WebhookErrors.WithLabelValues("validation").Inc()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Ensure there is no second JSON value in the body.
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		h.metrics.WebhookErrors.WithLabelValues("body").Inc()
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	receivedAt := time.Now()
	normalized, err := normalizeEvent(event, receivedAt)
	if err != nil {
		h.metrics.WebhookErrors.WithLabelValues("source").Inc()
		http.Error(w, "invalid source", http.StatusBadRequest)
		return
	}

	h.aggregator.Add(normalized)
	h.events.Add(event, normalized, receivedAt)
	h.metrics.EventBufferSize.Set(float64(h.events.Size()))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNoContent)
}

func validateEvent(e RoutingEvent) error {
	if strings.TrimSpace(e.Source) == "" {
		return fmt.Errorf("source is required")
	}
	if strings.TrimSpace(e.Destination) == "" && strings.TrimSpace(e.OriginalTarget) == "" {
		return fmt.Errorf("destination or originalTarget is required")
	}
	if strings.TrimSpace(e.OutboundTag) == "" {
		return fmt.Errorf("outboundTag is required")
	}
	return nil
}
