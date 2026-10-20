package main

import (
	"context"
	"sort"
	"sync"
	"time"
)

type Aggregator struct {
	mu sync.Mutex

	store   *Store
	metrics *Metrics

	// Minute buckets kept in RAM. Each event is aggregated immediately.
	minutes map[int64]map[AggKey]uint64

	// Pending hourly increments waiting for SQLite flush.
	pending map[HourKey]uint64

	cancel context.CancelFunc
	done   chan struct{}
}

func NewAggregator(store *Store, metrics *Metrics) *Aggregator {
	return &Aggregator{
		store:   store,
		metrics: metrics,
		minutes: make(map[int64]map[AggKey]uint64),
		pending: make(map[HourKey]uint64),
		done:    make(chan struct{}),
	}
}

func (a *Aggregator) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	a.cancel = cancel

	go a.flushLoop(ctx)
	go a.evictLoop(ctx)

	return nil
}

func (a *Aggregator) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	select {
	case <-a.done:
	case <-time.After(10 * time.Second):
	}

	// Best-effort final flush after loop termination.
	_ = a.flushPending()
}

func (a *Aggregator) Add(e NormalizedEvent) {
	a.mu.Lock()

	minute := e.Time.Truncate(time.Minute).Unix()
	bucket := a.minutes[minute]
	if bucket == nil {
		bucket = make(map[AggKey]uint64)
		a.minutes[minute] = bucket
	}
	bucket[aggKey(e)]++

	hk := hourKey(e)
	a.pending[hk]++

	pendingCount := uint64(0)
	for _, n := range a.pending {
		pendingCount += n
	}
	a.metrics.PendingEvents.Set(float64(pendingCount))
	a.refreshRAMMetricsLocked()
	a.mu.Unlock()

	a.metrics.EventsTotal.WithLabelValues(e.ClientIP, e.Outbound, e.Network, e.Protocol).Inc()
}

func (a *Aggregator) RecentHour(client string) []ReportRow {
	now := time.Now()
	cutoff := now.Add(-1 * time.Hour)

	a.mu.Lock()
	defer a.mu.Unlock()

	merged := make(map[AggKey]uint64)
	for bucketTime, bucket := range a.minutes {
		t := time.Unix(bucketTime, 0)
		if t.Before(cutoff.Truncate(time.Minute)) {
			continue
		}
		for key, n := range bucket {
			if client != "" && key.ClientIP != client {
				continue
			}
			merged[key] += n
		}
	}

	rows := make([]ReportRow, 0, len(merged))
	for key, n := range merged {
		rows = append(rows, ReportRow{
			ClientIP:    key.ClientIP,
			Destination: key.Destination,
			Outbound:    key.Outbound,
			Network:     key.Network,
			Protocol:    key.Protocol,
			Events:      n,
		})
	}
	return sortRows(rows)
}

func (a *Aggregator) RecentHourSummary(client string) []SummaryRow {
	rows := a.RecentHour(client)
	merged := make(map[string]SummaryRow, len(rows))
	for _, row := range rows {
		destination := destinationHost(row.Destination)
		key := summaryKey(row)
		item := merged[key]
		item.ClientIP = row.ClientIP
		item.Destination = destination
		item.Outbound = row.Outbound
		item.Events += row.Events
		merged[key] = item
	}

	result := make([]SummaryRow, 0, len(merged))
	for _, row := range merged {
		result = append(result, row)
	}
	sortSummaryRows(result)
	return result
}

func sortSummaryRows(rows []SummaryRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Events != rows[j].Events {
			return rows[i].Events > rows[j].Events
		}
		if rows[i].ClientIP != rows[j].ClientIP {
			return rows[i].ClientIP < rows[j].ClientIP
		}
		if rows[i].Destination != rows[j].Destination {
			return rows[i].Destination < rows[j].Destination
		}
		return rows[i].Outbound < rows[j].Outbound
	})
}

func (a *Aggregator) flushLoop(ctx context.Context) {
	ticker := time.NewTicker(a.store.flushEvery)
	defer ticker.Stop()
	defer close(a.done)

	for {
		select {
		case <-ctx.Done():
			_ = a.flushPending()
			return
		case <-ticker.C:
			if err := a.flushPending(); err != nil {
				// Error is already reflected in Prometheus; keep the daemon alive.
			}
			_ = a.store.DeleteOld(context.Background(), time.Now())
		}
	}
}

func (a *Aggregator) evictLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			a.mu.Lock()
			cutoff := now.Add(-1 * time.Hour).Truncate(time.Minute).Unix()
			for bucketTime := range a.minutes {
				if bucketTime < cutoff {
					delete(a.minutes, bucketTime)
				}
			}
			a.refreshRAMMetricsLocked()
			a.mu.Unlock()
		}
	}
}

func (a *Aggregator) flushPending() error {
	a.mu.Lock()
	if len(a.pending) == 0 {
		a.mu.Unlock()
		return nil
	}

	batch := make(map[HourKey]uint64, len(a.pending))
	for key, n := range a.pending {
		batch[key] = n
	}
	a.pending = make(map[HourKey]uint64)
	a.metrics.PendingEvents.Set(0)
	a.mu.Unlock()

	if err := a.store.Upsert(batch); err != nil {
		a.mu.Lock()
		for key, n := range batch {
			a.pending[key] += n
		}
		pendingCount := uint64(0)
		for _, n := range a.pending {
			pendingCount += n
		}
		a.metrics.PendingEvents.Set(float64(pendingCount))
		a.mu.Unlock()

		a.metrics.SQLiteErrors.Inc()
		return err
	}

	a.metrics.LastFlush.SetToCurrentTime()
	return nil
}

func (a *Aggregator) refreshRAMMetricsLocked() {
	series := 0
	clients := make(map[string]struct{})
	for _, bucket := range a.minutes {
		series += len(bucket)
		for key := range bucket {
			clients[key.ClientIP] = struct{}{}
		}
	}
	a.metrics.RAMSeries.Set(float64(series))
	a.metrics.RAMClients.Set(float64(len(clients)))
}
