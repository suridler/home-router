package main

import (
	"net"
	"strconv"
	"strings"
	"time"
)

// RoutingEvent matches the Routing Webhook payload emitted by Xray 26.3.27.
type RoutingEvent struct {
	Email          *string `json:"email"`
	Level          int     `json:"level"`
	Protocol       string  `json:"protocol"`
	Network        string  `json:"network"`
	Source         string  `json:"source"`
	Destination    string  `json:"destination"`
	OriginalTarget string  `json:"originalTarget"`
	RouteTarget    *string `json:"routeTarget"`
	InboundTag     string  `json:"inboundTag"`
	InboundName    string  `json:"inboundName"`
	InboundLocal   string  `json:"inboundLocal"`
	OutboundTag    string  `json:"outboundTag"`
	TS             int64   `json:"ts"`
}

type NormalizedEvent struct {
	Time           time.Time
	ClientIP       string
	ClientPort     uint16
	Destination    string
	OriginalTarget string
	Protocol       string
	Network        string
	Inbound        string
	Outbound       string
}

type AggKey struct {
	ClientIP    string
	Destination string
	Outbound    string
	Network     string
	Protocol    string
}

type HourKey struct {
	Bucket      int64
	ClientIP    string
	Destination string
	Outbound    string
	Network     string
	Protocol    string
}

type ReportRow struct {
	ClientIP    string `json:"client_ip"`
	Destination string `json:"destination"`
	Outbound    string `json:"outbound"`
	Network     string `json:"network"`
	Protocol    string `json:"protocol,omitempty"`
	Events      uint64 `json:"events"`
}

type SummaryRow struct {
	ClientIP    string `json:"client_ip"`
	Destination string `json:"destination"`
	Outbound    string `json:"outbound"`
	Events      uint64 `json:"events"`
}

func normalizeEvent(e RoutingEvent, now time.Time) (NormalizedEvent, error) {
	clientIP, clientPort, err := parseHostPort(e.Source)
	if err != nil {
		return NormalizedEvent{}, err
	}

	t := now
	if e.TS > 0 {
		candidate := time.Unix(e.TS, 0)
		// Reject obviously broken timestamps; otherwise use Xray's timestamp.
		if candidate.After(now.Add(-48*time.Hour)) && candidate.Before(now.Add(5*time.Minute)) {
			t = candidate
		}
	}

	destination := strings.TrimSpace(e.Destination)
	if destination == "" {
		destination = strings.TrimSpace(e.OriginalTarget)
	}

	return NormalizedEvent{
		Time:           t,
		ClientIP:       clientIP,
		ClientPort:     clientPort,
		Destination:    destination,
		OriginalTarget: strings.TrimSpace(e.OriginalTarget),
		Protocol:       strings.TrimSpace(e.Protocol),
		Network:        strings.TrimSpace(e.Network),
		Inbound:        strings.TrimSpace(e.InboundTag),
		Outbound:       strings.TrimSpace(e.OutboundTag),
	}, nil
}

func parseHostPort(value string) (string, uint16, error) {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return "", 0, err
	}
	portNum, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return "", 0, err
	}
	return host, uint16(portNum), nil
}

func destinationHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return strings.Trim(host, "[]")
	}
	if net.ParseIP(value) != nil {
		return strings.Trim(value, "[]")
	}
	return strings.Trim(value, "[]")
}

func summaryKey(row ReportRow) string {
	return row.ClientIP + "\x00" + destinationHost(row.Destination) + "\x00" + row.Outbound
}

func aggKey(e NormalizedEvent) AggKey {
	return AggKey{
		ClientIP:    e.ClientIP,
		Destination: e.Destination,
		Outbound:    e.Outbound,
		Network:     e.Network,
		Protocol:    e.Protocol,
	}
}

func hourKey(e NormalizedEvent) HourKey {
	return HourKey{
		Bucket:      e.Time.Truncate(time.Hour).Unix(),
		ClientIP:    e.ClientIP,
		Destination: e.Destination,
		Outbound:    e.Outbound,
		Network:     e.Network,
		Protocol:    e.Protocol,
	}
}

// nonNegativeInt64 clamps negative values to zero. Xray StatsService exposes
// counters as int64; traffic counters are expected to be non-negative.
func nonNegativeInt64(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
