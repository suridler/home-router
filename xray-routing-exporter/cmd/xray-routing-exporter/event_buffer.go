package main

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// RoutingEventRecord is a single accepted Xray routing event kept in the
// in-memory ring buffer for the pseudo-stream API.
type RoutingEventRecord struct {
	ID             uint64    `json:"id"`
	DateTime       time.Time `json:"date_time"`
	ReceivedAt     time.Time `json:"received_at"`
	Email          *string   `json:"email"`
	Level          int       `json:"level"`
	Protocol       string    `json:"protocol"`
	Network        string    `json:"network"`
	Source         string    `json:"source"`
	ClientIP       string    `json:"client_ip"`
	ClientPort     uint16    `json:"client_port"`
	Destination    string    `json:"destination"`
	OriginalTarget string    `json:"originalTarget"`
	RouteTarget    *string   `json:"routeTarget"`
	InboundTag     string    `json:"inboundTag"`
	InboundName    string    `json:"inboundName"`
	InboundLocal   string    `json:"inboundLocal"`
	OutboundTag    string    `json:"outboundTag"`
	TS             int64     `json:"ts"`
}

type EventBuffer struct {
	mu       sync.RWMutex
	capacity int
	nextID   uint64
	events   []RoutingEventRecord
}

func NewEventBuffer(capacity int) *EventBuffer {
	if capacity < 1 {
		capacity = 2000
	}
	return &EventBuffer{
		capacity: capacity,
		events:   make([]RoutingEventRecord, 0, capacity),
		nextID:   1,
	}
}

func (b *EventBuffer) Add(raw RoutingEvent, normalized NormalizedEvent, receivedAt time.Time) RoutingEventRecord {
	b.mu.Lock()
	defer b.mu.Unlock()

	record := RoutingEventRecord{
		ID:             b.nextID,
		DateTime:       normalized.Time.UTC(),
		ReceivedAt:     receivedAt.UTC(),
		Email:          raw.Email,
		Level:          raw.Level,
		Protocol:       raw.Protocol,
		Network:        raw.Network,
		Source:         raw.Source,
		ClientIP:       normalized.ClientIP,
		ClientPort:     normalized.ClientPort,
		Destination:    raw.Destination,
		OriginalTarget: raw.OriginalTarget,
		RouteTarget:    raw.RouteTarget,
		InboundTag:     raw.InboundTag,
		InboundName:    raw.InboundName,
		InboundLocal:   raw.InboundLocal,
		OutboundTag:    raw.OutboundTag,
		TS:             raw.TS,
	}
	b.nextID++

	b.events = append(b.events, record)
	if len(b.events) > b.capacity {
		b.events = b.events[len(b.events)-b.capacity:]
	}
	return record
}

func (b *EventBuffer) Query(client, outboundTag string, afterTimestamp int64, hasTimestamp bool, limit int) ([]RoutingEventRecord, time.Time, time.Time, int) {
	if limit < 1 {
		limit = 100
	}
	b.mu.RLock()
	defer b.mu.RUnlock()

	filtered := make([]RoutingEventRecord, 0, len(b.events))
	client = strings.TrimSpace(client)
	outboundTag = strings.TrimSpace(outboundTag)
	for _, event := range b.events {
		if client != "" && event.ClientIP != client {
			continue
		}
		if outboundTag != "" && event.OutboundTag != outboundTag {
			continue
		}
		if hasTimestamp && event.DateTime.Unix() <= afterTimestamp {
			continue
		}
		filtered = append(filtered, event)
	}

	// The ring is naturally chronological, but keep the contract explicit.
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].DateTime.Equal(filtered[j].DateTime) {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].DateTime.Before(filtered[j].DateTime)
	})

	if len(filtered) > limit {
		if hasTimestamp {
			filtered = filtered[:limit]
		} else {
			filtered = filtered[len(filtered)-limit:]
		}
	}

	var oldest, newest time.Time
	for _, event := range b.events {
		if oldest.IsZero() || event.DateTime.Before(oldest) {
			oldest = event.DateTime
		}
		if newest.IsZero() || event.DateTime.After(newest) {
			newest = event.DateTime
		}
	}
	return filtered, oldest, newest, len(b.events)
}

func (b *EventBuffer) Size() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.events)
}

func (b *EventBuffer) Capacity() int {
	return b.capacity
}
