package main

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	WebhookRequests *prometheus.CounterVec
	WebhookErrors   *prometheus.CounterVec
	SQLiteErrors    prometheus.Counter
	EventsTotal     *prometheus.CounterVec
	RAMSeries       prometheus.Gauge
	RAMClients      prometheus.Gauge
	PendingEvents   prometheus.Gauge
	LastFlush       prometheus.Gauge
	EventBufferSize prometheus.Gauge

	XrayTraffic           *prometheus.CounterVec
	XrayAPIUp             prometheus.Gauge
	XrayQueryErrors       prometheus.Counter
	XrayQueries           prometheus.Counter
	XrayLastQuery         prometheus.Gauge
	XrayLastQueryDuration prometheus.Gauge
	XrayStateErrors       prometheus.Counter
	XrayUptime            prometheus.Gauge
	XrayStartTimestamp    prometheus.Gauge
	XrayRestartTotal      prometheus.Counter
}

func NewMetrics() *Metrics {
	m := &Metrics{
		WebhookRequests: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "xray_routing_webhook_requests_total", Help: "Total HTTP requests received on the Xray routing webhook socket."},
			[]string{"method", "path"},
		),
		WebhookErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "xray_routing_webhook_errors_total", Help: "Total rejected or malformed webhook requests."},
			[]string{"reason"},
		),
		SQLiteErrors: prometheus.NewCounter(
			prometheus.CounterOpts{Name: "xray_routing_sqlite_flush_errors_total", Help: "Total SQLite flush failures."},
		),
		EventsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "xray_routing_events_total", Help: "Total accepted routing events by low-cardinality dimensions."},
			[]string{"client", "outbound", "network", "protocol"},
		),
		RAMSeries: prometheus.NewGauge(
			prometheus.GaugeOpts{Name: "xray_routing_ram_series", Help: "Number of distinct routing aggregation keys currently kept in RAM."},
		),
		RAMClients: prometheus.NewGauge(
			prometheus.GaugeOpts{Name: "xray_routing_ram_clients", Help: "Number of distinct clients currently present in the RAM window."},
		),
		PendingEvents: prometheus.NewGauge(
			prometheus.GaugeOpts{Name: "xray_routing_pending_sqlite_events", Help: "Number of routing events aggregated but not yet flushed to SQLite."},
		),
		EventBufferSize: prometheus.NewGauge(
			prometheus.GaugeOpts{Name: "xray_routing_event_buffer_size", Help: "Number of individual routing events currently kept in the RAM ring buffer."},
		),
		LastFlush: prometheus.NewGauge(
			prometheus.GaugeOpts{Name: "xray_routing_last_sqlite_flush_timestamp_seconds", Help: "Unix timestamp of the last successful SQLite flush."},
		),
		XrayTraffic: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "xray_traffic_bytes_total", Help: "Persistent cumulative Xray traffic bytes reconstructed from Xray runtime counters."},
			[]string{"scope", "tag", "direction"},
		),
		XrayAPIUp: prometheus.NewGauge(
			prometheus.GaugeOpts{Name: "xray_stats_api_up", Help: "Whether the last Xray StatsService query succeeded."},
		),
		XrayQueryErrors: prometheus.NewCounter(
			prometheus.CounterOpts{Name: "xray_stats_query_errors_total", Help: "Total failed Xray StatsService queries."},
		),
		XrayQueries: prometheus.NewCounter(
			prometheus.CounterOpts{Name: "xray_stats_queries_total", Help: "Total successful Xray StatsService queries."},
		),
		XrayLastQuery: prometheus.NewGauge(
			prometheus.GaugeOpts{Name: "xray_stats_last_query_timestamp_seconds", Help: "Unix timestamp of the last successful Xray StatsService query."},
		),
		XrayLastQueryDuration: prometheus.NewGauge(
			prometheus.GaugeOpts{Name: "xray_stats_last_query_duration_seconds", Help: "Duration of the last Xray StatsService query."},
		),
		XrayStateErrors: prometheus.NewCounter(
			prometheus.CounterOpts{Name: "xray_stats_state_persist_errors_total", Help: "Total failures while persisting Xray runtime/counter state."},
		),
		XrayUptime: prometheus.NewGauge(
			prometheus.GaugeOpts{Name: "xray_uptime_seconds", Help: "Current Xray process uptime in seconds."},
		),
		XrayStartTimestamp: prometheus.NewGauge(
			prometheus.GaugeOpts{Name: "xray_start_timestamp_seconds", Help: "Unix timestamp of the current Xray process start, derived from uptime."},
		),
		XrayRestartTotal: prometheus.NewCounter(
			prometheus.CounterOpts{Name: "xray_restart_total", Help: "Persistent count of detected Xray restarts observed by this collector."},
		),
	}

	prometheus.MustRegister(
		m.WebhookRequests, m.WebhookErrors, m.SQLiteErrors, m.EventsTotal,
		m.RAMSeries, m.RAMClients, m.PendingEvents, m.LastFlush, m.EventBufferSize,
		m.XrayTraffic, m.XrayAPIUp, m.XrayQueryErrors, m.XrayQueries,
		m.XrayLastQuery, m.XrayLastQueryDuration, m.XrayStateErrors,
		m.XrayUptime, m.XrayStartTimestamp, m.XrayRestartTotal,
	)

	return m
}
