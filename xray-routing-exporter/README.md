# xray-routing-exporter

Go collector v0.3.5 for Xray 26.3.27 Routing Webhook and StatsService.

## Features

- Unix Domain Socket HTTP receiver for Xray Routing Webhook.
- Parsing of Xray 26.3.27 webhook payload.
- In-memory minute aggregation for the last 60 minutes.
- SQLite hourly aggregation with rolling 30-day retention.
- Detail and summary JSON APIs.
- In-memory ring buffer of individual routing events with polling-friendly events API.
- Prometheus metrics with low-cardinality labels.
- Xray StatsService polling every 15 seconds by default.
- Persistent reconstruction of Xray traffic counters across Xray restarts.
- Persistent Xray uptime/start time/restart count state.
- Detection of Xray restarts via uptime decrease.
- Persistent Prometheus cumulative traffic counters restored after collector restart.
- Unknown webhook JSON fields are ignored for forward compatibility.

## Architecture

```text
Xray
├── Routing Webhook ──Unix socket──> collector
└── StatsService ─────gRPC─────────> collector
                                      ├── RAM: 60m routing events
                                      ├── SQLite: 30d routing aggregates
                                      └── Prometheus: routing + Xray metrics
```

`nftables` remains outside the collector and is used by the gateway for TProxy/loop prevention.

The exporter includes a bounded read-only Unix-socket client for
`GET /v1/xray/exporter-metadata`. It strictly parses the helper's Xray API
address, inbound/outbound tags, generation and content hash without logging
response bodies. Additional response fields are accepted so compatible helper
updates do not require an exporter update. Known fields remain validated and
malformed or trailing JSON is rejected. A standalone metadata manager atomically persists the last
valid snapshot with mode `0600`, retains it as stale during helper outages,
refreshes it on a bounded interval, and applies deterministic capped backoff
after failures. StatsService polling uses the metadata address directly and
allows only the known inbound/outbound tags to create Prometheus label series.
A temporary helper outage retains both the cached polling address and tag
allowlist; Routing Webhook ingestion remains independent.

## Build

Requires Go 1.26+.

```bash
go build -trimpath -ldflags='-s -w' -o xray-routing-exporter ./cmd/xray-routing-exporter
```

## Manual run

```bash
./xray-routing-exporter \
  --socket=/tmp/xray-routing.sock \
  --http=127.0.0.1:9101 \
  --db=/tmp/xray-routing.db \
  --helper-socket=/run/xray-core-helper/helper.sock \
  --helper-metadata-cache=/tmp/xray-helper-metadata.json \
  --xray-stats-interval=15s
```

Xray webhook:

```json
"webhook": {
  "url": "/tmp/xray-routing.sock:/webhook",
  "deduplication": 0
}
```

## Production

The supplied systemd unit uses:

```text
/run/xray-routing-exporter/routing-webhook.sock
/var/lib/xray-routing-exporter/routing.db
/var/lib/xray-routing-exporter/helper-metadata.json
127.0.0.1:9101
/run/xray-core-helper/helper.sock
```

The collector runs as `xray:xray`, receives the `xray-core-helper`
supplementary group to consume helper metadata, and creates the webhook socket
with mode `0660`.

`GET /healthz` reports process liveness. `GET /readyz` requires both telemetry
SQLite and an initial valid helper metadata snapshot. Missing helper metadata
returns `503` with `code: metadata_unavailable` without stopping webhook
ingestion or deleting restored telemetry. A valid cached snapshot is ready and
is reported as `metadata: stale` until the helper refresh succeeds. The Xray
StatsService address is no longer configured independently in exporter flags.

Set all production Xray routing webhooks to:

```json
"url": "/run/xray-routing-exporter/routing-webhook.sock:/webhook"
```

## JSON API

Last hour detail:

```bash
curl 'http://127.0.0.1:9101/api/v1/stats/hour?client=192.0.2.51'
```

Last hour summary (destination host + outbound):

```bash
curl 'http://127.0.0.1:9101/api/v1/stats/hour/summary?client=192.0.2.51'
```

Last 30 days detail:

```bash
curl 'http://127.0.0.1:9101/api/v1/stats/30d?client=192.0.2.51'
```

Last 30 days summary:

```bash
curl 'http://127.0.0.1:9101/api/v1/stats/30d/summary?client=192.0.2.51'
```

Current persistent Xray state:

```bash
curl 'http://127.0.0.1:9101/api/v1/xray/stats'
```

Prometheus:

```bash
curl 'http://127.0.0.1:9101/metrics'
```

## Xray StatsService semantics

The collector calls `QueryStats` with `reset=false` (`Reset_` in the generated Go API) and `GetSysStats` every 15 seconds by default.

Xray traffic counters are runtime counters and can reset when Xray restarts. The collector stores the previous raw value and a persistent cumulative value in SQLite:

```text
current >= previous  -> delta = current - previous
current <  previous  -> counter reset; delta = current
cumulative += delta
```

The collector also persists Xray uptime and restart count. A decrease in Xray uptime is treated as a process restart.

On collector startup, persisted cumulative counters and restart count are loaded into the Prometheus CounterVec, so restarting the collector itself does not make the exported historical total appear to start from zero.

The raw Xray counters are intentionally not reset by the collector.

## Routing event semantics

`events` means Xray Routing Webhook events. It is not a byte count and should not automatically be interpreted as HTTP requests. UDP can generate repeated routing events.

Detail aggregation key:

```text
client + destination:port + outbound + network + protocol
```

Summary aggregation key:

```text
client + destination host/IP + outbound
```

### Individual routing events API

The collector also keeps the latest individual accepted routing events in a RAM ring buffer. The default capacity is 2000 events and can be changed with `--event-buffer-size`. These events are not written individually to SQLite.

Get the latest events:

```bash
curl 'http://127.0.0.1:9101/api/v1/events?limit=100'
```

Optional client filter:

```bash
curl 'http://127.0.0.1:9101/api/v1/events?client=192.0.2.51&limit=100'
```

Optional outbound filter:

```bash
curl 'http://127.0.0.1:9101/api/v1/events?outboundTag=block&limit=100'
```

Optional Unix timestamp filter. The endpoint returns events whose normalized event timestamp is strictly greater than the supplied Unix timestamp in seconds:

```bash
curl 'http://127.0.0.1:9101/api/v1/events?timestamp=1790177580&limit=100'
```

The filters can be combined:

```bash
curl 'http://127.0.0.1:9101/api/v1/events?client=192.0.2.51&outboundTag=block&timestamp=1790177580&limit=100'
```

`client`, `outboundTag`, and `timestamp` are all optional. `limit` defaults to 100 and is capped at 1000.

The response includes the applied filters plus `buffer_size`, `buffer_capacity`, `oldest_event`, `newest_event`, and the individual `events` array.

`timestamp` is intended as a polling cursor for a pseudo-stream. It is an exclusive lower time bound. Because the timestamp has one-second resolution, it is not an exactly-once cursor when multiple events can share the same second. A future `after_id` cursor can be added if exact cursor semantics are needed.

## Project documentation

See [architecture](../docs/ARCHITECTURE.md),
[installation](../docs/INSTALLATION.md), [operations](../docs/OPERATIONS.md)
and [verification](../docs/TESTING.md). The helper bundle does not install
this exporter; review its supplied service unit and host paths separately.
Access to the helper socket is currently trusted, not enforced as a distinct
read-only role for the exporter.
