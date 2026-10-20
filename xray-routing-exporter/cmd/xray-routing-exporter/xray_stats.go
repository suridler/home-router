package main

import (
	"context"
	"strings"
	"sync"
	"time"

	xraystats "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type XrayStatKey struct {
	Name      string
	Scope     string
	Tag       string
	Direction string
}

type XrayRuntimeState struct {
	UptimeSeconds uint32
	StartUnix     int64
	RestartCount  uint64
	UpdatedAt     int64
}

type XrayStatsSnapshot struct {
	CollectedAt  time.Time             `json:"collected_at"`
	Up           bool                  `json:"up"`
	QueryErrors  uint64                `json:"query_errors"`
	QueryMS      float64               `json:"query_duration_ms"`
	Uptime       uint32                `json:"uptime_seconds"`
	StartUnix    int64                 `json:"start_unix"`
	RestartCount uint64                `json:"restart_count"`
	Counters     []XrayCounterSnapshot `json:"counters"`
	Error        string                `json:"error,omitempty"`
}

type XrayCounterSnapshot struct {
	Key             XrayStatKey `json:"key"`
	RawBytes        uint64      `json:"raw_bytes"`
	CumulativeBytes uint64      `json:"cumulative_bytes"`
}

type XrayStatsCollector struct {
	metadata *MetadataManager
	interval time.Duration
	store    *Store
	metrics  *Metrics
	logger   Logger

	mu       sync.RWMutex
	snapshot XrayStatsSnapshot
	cancel   context.CancelFunc
	done     chan struct{}
}

type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

func NewXrayStatsCollector(metadata *MetadataManager, interval time.Duration, store *Store, metrics *Metrics, logger Logger) *XrayStatsCollector {
	return &XrayStatsCollector{
		metadata: metadata, interval: interval, store: store, metrics: metrics, logger: logger,
		done: make(chan struct{}),
	}
}

func (c *XrayStatsCollector) Initialize() error {
	states, err := c.store.LoadXrayCounterStates()
	if err != nil {
		return err
	}
	for _, state := range states {
		c.metrics.XrayTraffic.WithLabelValues(state.Key.Scope, state.Key.Tag, state.Key.Direction).Add(float64(state.CumulativeValue))
	}

	runtimeState, exists, err := c.store.LoadXrayRuntime()
	if err != nil {
		return err
	}
	if exists {
		c.metrics.XrayRestartTotal.Add(float64(runtimeState.RestartCount))
		c.metrics.XrayUptime.Set(float64(runtimeState.UptimeSeconds))
		c.metrics.XrayStartTimestamp.Set(float64(runtimeState.StartUnix))
	}
	return nil
}

func (c *XrayStatsCollector) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	go c.loop(ctx)
}

func (c *XrayStatsCollector) Stop() {
	if c.cancel == nil {
		return
	}
	c.cancel()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
	}
}

func (c *XrayStatsCollector) Snapshot() XrayStatsSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cp := c.snapshot
	cp.Counters = append([]XrayCounterSnapshot(nil), c.snapshot.Counters...)
	return cp
}

func (c *XrayStatsCollector) loop(ctx context.Context) {
	defer close(c.done)
	c.collect(ctx)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collect(ctx)
		}
	}
}

func (c *XrayStatsCollector) collect(parent context.Context) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	metadata := MetadataSnapshot{}
	if c.metadata != nil {
		if _, err := c.metadata.Refresh(ctx, started); err != nil {
			c.logger.Warn("refresh helper metadata failed; retaining last valid snapshot", "err", err)
		}
		metadata = c.metadata.Snapshot()
	}
	if !metadata.Available {
		c.recordMetadataUnavailable()
		return
	}
	addr := metadata.Metadata.API

	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		c.recordFailure(time.Since(started), err)
		return
	}
	defer conn.Close()

	client := xraystats.NewStatsServiceClient(conn)
	resp, err := client.QueryStats(ctx, &xraystats.QueryStatsRequest{Pattern: "", Reset_: false})
	if err != nil {
		c.recordFailure(time.Since(started), err)
		return
	}

	sys, err := client.GetSysStats(ctx, &xraystats.SysStatsRequest{})
	if err != nil {
		c.recordFailure(time.Since(started), err)
		return
	}

	now := time.Now().UTC()
	startUnix := now.Unix() - int64(sys.GetUptime())
	restart, restartCount, err := c.store.ApplyXrayRuntime(XrayRuntimeState{
		UptimeSeconds: sys.GetUptime(),
		StartUnix:     startUnix,
		UpdatedAt:     now.Unix(),
	})
	if err != nil {
		c.metrics.XrayStateErrors.Inc()
		c.recordFailure(time.Since(started), err)
		return
	}

	counters := make([]XrayCounterSnapshot, 0, len(resp.GetStat()))
	for _, stat := range resp.GetStat() {
		if stat == nil {
			continue
		}
		key, ok := parseXrayTrafficName(stat.GetName())
		if !ok || !metadataAllowsStat(metadata, key) {
			continue
		}

		cumulative, delta, counterReset, err := c.store.ApplyXrayCounter(key, stat.GetValue(), now.Unix())
		if err != nil {
			c.metrics.XrayStateErrors.Inc()
			c.recordFailure(time.Since(started), err)
			return
		}

		counters = append(counters, XrayCounterSnapshot{
			Key: key, RawBytes: uint64(nonNegativeInt64(stat.GetValue())), CumulativeBytes: cumulative,
		})

		if delta > 0 {
			c.metrics.XrayTraffic.WithLabelValues(key.Scope, key.Tag, key.Direction).Add(float64(delta))
		}
		if counterReset {
			c.logger.Info("xray traffic counter reset detected", "name", key.Name)
		}
	}

	elapsed := time.Since(started)
	c.metrics.XrayAPIUp.Set(1)
	c.metrics.XrayLastQuery.SetToCurrentTime()
	c.metrics.XrayLastQueryDuration.Set(elapsed.Seconds())
	c.metrics.XrayUptime.Set(float64(sys.GetUptime()))
	c.metrics.XrayStartTimestamp.Set(float64(startUnix))
	if restart {
		c.metrics.XrayRestartTotal.Inc()
		c.logger.Info("xray restart detected", "uptime", sys.GetUptime(), "restart_count", restartCount)
	}
	c.metrics.XrayQueries.Inc()

	c.mu.Lock()
	c.snapshot = XrayStatsSnapshot{
		CollectedAt:  now,
		Up:           true,
		Uptime:       sys.GetUptime(),
		StartUnix:    startUnix,
		RestartCount: restartCount,
		QueryMS:      elapsed.Seconds() * 1000,
		Counters:     counters,
	}
	c.mu.Unlock()
}

func metadataAllowsStat(snapshot MetadataSnapshot, key XrayStatKey) bool {
	if !snapshot.Available {
		return false
	}
	var tags []string
	switch key.Scope {
	case "inbound":
		tags = snapshot.Metadata.InboundTags
	case "outbound":
		tags = snapshot.Metadata.OutboundTags
	default:
		return false
	}
	for _, tag := range tags {
		if key.Tag == tag {
			return true
		}
	}
	return false
}

func (c *XrayStatsCollector) recordMetadataUnavailable() {
	c.metrics.XrayAPIUp.Set(0)
	c.metrics.XrayQueryErrors.Inc()
	c.mu.Lock()
	c.snapshot.CollectedAt = time.Now().UTC()
	c.snapshot.Up = false
	c.snapshot.QueryErrors++
	c.snapshot.QueryMS = 0
	c.snapshot.Error = ErrMetadataUnavailable.Error()
	c.mu.Unlock()
}

func (c *XrayStatsCollector) recordFailure(elapsed time.Duration, err error) {
	c.metrics.XrayAPIUp.Set(0)
	c.metrics.XrayQueryErrors.Inc()
	c.metrics.XrayLastQueryDuration.Set(elapsed.Seconds())
	c.logger.Warn("xray stats query failed", "err", err)

	c.mu.Lock()
	c.snapshot = XrayStatsSnapshot{
		CollectedAt: time.Now().UTC(),
		Up:          false,
		QueryErrors: 1,
		QueryMS:     elapsed.Seconds() * 1000,
		Error:       err.Error(),
	}
	c.mu.Unlock()
}

func xrayCounterDelta(previous, current int64) (uint64, bool) {
	if current < 0 {
		return 0, true
	}
	if previous < 0 {
		previous = 0
	}
	if current >= previous {
		return uint64(current - previous), false
	}
	return uint64(current), true
}

func parseXrayTrafficName(name string) (XrayStatKey, bool) {
	parts := strings.Split(name, ">>>")
	if len(parts) != 4 || parts[2] != "traffic" {
		return XrayStatKey{}, false
	}
	if parts[0] != "inbound" && parts[0] != "outbound" {
		return XrayStatKey{}, false
	}
	if parts[3] != "uplink" && parts[3] != "downlink" {
		return XrayStatKey{}, false
	}
	return XrayStatKey{Name: name, Scope: parts[0], Tag: parts[1], Direction: parts[3]}, true
}
