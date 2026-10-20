package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db         *sql.DB
	retention  time.Duration
	flushEvery time.Duration
}

func NewStore(path string, retention, flushEvery time.Duration) (*Store, error) {
	if retention <= 0 {
		return nil, fmt.Errorf("retention must be > 0")
	}
	if flushEvery <= 0 {
		return nil, fmt.Errorf("flush interval must be > 0")
	}

	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db, retention: retention, flushEvery: flushEvery}
	if err := store.init(); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.DeleteOld(context.Background(), time.Now()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) init() error {
	const schema = `
CREATE TABLE IF NOT EXISTS routing_stats (
    bucket_time  INTEGER NOT NULL,
    client_ip    TEXT NOT NULL,
    destination  TEXT NOT NULL,
    outbound     TEXT NOT NULL,
    network      TEXT NOT NULL,
    protocol     TEXT NOT NULL,
    event_count  INTEGER NOT NULL,
    PRIMARY KEY (bucket_time, client_ip, destination, outbound, network, protocol)
);
CREATE INDEX IF NOT EXISTS idx_routing_stats_bucket
    ON routing_stats(bucket_time);
CREATE INDEX IF NOT EXISTS idx_routing_stats_client
    ON routing_stats(client_ip, bucket_time);
CREATE INDEX IF NOT EXISTS idx_routing_stats_destination
    ON routing_stats(destination, bucket_time);

CREATE TABLE IF NOT EXISTS xray_counters (
    name            TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    tag             TEXT NOT NULL,
    direction       TEXT NOT NULL,
    raw_value       INTEGER NOT NULL,
    cumulative_value INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS xray_runtime (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    uptime_seconds  INTEGER NOT NULL,
    start_unix      INTEGER NOT NULL,
    restart_count   INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
`
	_, err := s.db.Exec(schema)
	return err
}

func (s *Store) Upsert(batch map[HourKey]uint64) error {
	if len(batch) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO routing_stats
    (bucket_time, client_ip, destination, outbound, network, protocol, event_count)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(bucket_time, client_ip, destination, outbound, network, protocol)
DO UPDATE SET event_count = event_count + excluded.event_count
`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for key, count := range batch {
		if _, err := stmt.ExecContext(ctx,
			key.Bucket,
			key.ClientIP,
			key.Destination,
			key.Outbound,
			key.Network,
			key.Protocol,
			count,
		); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	return s.DeleteOld(ctx, time.Now())
}

func (s *Store) DeleteOld(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-s.retention).Truncate(time.Hour).Unix()
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM routing_stats WHERE bucket_time < ?`, cutoff)
	return err
}

func (s *Store) Last30Days(client string) ([]ReportRow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query := `
SELECT client_ip, destination, outbound, network, protocol, SUM(event_count) AS events
FROM routing_stats
`
	args := []any{}
	if client != "" {
		query += "WHERE client_ip = ?\n"
		args = append(args, client)
	}
	query += `GROUP BY client_ip, destination, outbound, network, protocol
ORDER BY events DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]ReportRow, 0)
	for rows.Next() {
		var row ReportRow
		if err := rows.Scan(&row.ClientIP, &row.Destination, &row.Outbound, &row.Network, &row.Protocol, &row.Events); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) Last30DaysSummary(client string) ([]SummaryRow, error) {
	rows, err := s.Last30Days(client)
	if err != nil {
		return nil, err
	}

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
	return result, nil
}

func sortRows(rows []ReportRow) []ReportRow {
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
	return rows
}

type XrayStoredState struct {
	Key             XrayStatKey
	RawValue        int64
	CumulativeValue uint64
	UpdatedAt       int64
}

func (s *Store) LoadXrayCounterStates() ([]XrayStoredState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
SELECT name, scope, tag, direction, raw_value, cumulative_value, updated_at
FROM xray_counters`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]XrayStoredState, 0)
	for rows.Next() {
		var name, scope, tag, direction string
		var raw, cumulative, updated int64
		if err := rows.Scan(&name, &scope, &tag, &direction, &raw, &cumulative, &updated); err != nil {
			return nil, err
		}
		result = append(result, XrayStoredState{
			Key:             XrayStatKey{Name: name, Scope: scope, Tag: tag, Direction: direction},
			RawValue:        raw,
			CumulativeValue: uint64(nonNegativeInt64(cumulative)),
			UpdatedAt:       updated,
		})
	}
	return result, rows.Err()
}

func (s *Store) LoadXrayRuntime() (XrayRuntimeState, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var state XrayRuntimeState
	var restartCount int64
	err := s.db.QueryRowContext(ctx, `
SELECT uptime_seconds, start_unix, restart_count, updated_at
FROM xray_runtime WHERE id = 1`).Scan(
		&state.UptimeSeconds, &state.StartUnix, &restartCount, &state.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return XrayRuntimeState{}, false, nil
	}
	if err != nil {
		return XrayRuntimeState{}, false, err
	}
	state.RestartCount = uint64(nonNegativeInt64(restartCount))
	return state, true, nil
}

func (s *Store) ApplyXrayRuntime(current XrayRuntimeState) (bool, uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	previous, exists, err := s.LoadXrayRuntime()
	if err != nil {
		return false, 0, err
	}

	restarted := false
	restartCount := current.RestartCount
	if exists {
		restartCount = previous.RestartCount
		if current.UptimeSeconds < previous.UptimeSeconds {
			restarted = true
			restartCount++
		}
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO xray_runtime (id, uptime_seconds, start_unix, restart_count, updated_at)
VALUES (1, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    uptime_seconds = excluded.uptime_seconds,
    start_unix = excluded.start_unix,
    restart_count = excluded.restart_count,
    updated_at = excluded.updated_at`,
		current.UptimeSeconds,
		current.StartUnix,
		restartCount,
		current.UpdatedAt,
	)
	if err != nil {
		return false, restartCount, err
	}
	return restarted, restartCount, nil
}

func (s *Store) ApplyXrayCounter(key XrayStatKey, rawValue int64, nowUnix int64) (cumulative uint64, delta uint64, reset bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var previousRaw, previousCumulative int64
	err = s.db.QueryRowContext(ctx, `
SELECT raw_value, cumulative_value
FROM xray_counters WHERE name = ?`, key.Name).Scan(&previousRaw, &previousCumulative)

	if err == sql.ErrNoRows {
		n := nonNegativeInt64(rawValue)
		_, err = s.db.ExecContext(ctx, `
INSERT INTO xray_counters (name, scope, tag, direction, raw_value, cumulative_value, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			key.Name, key.Scope, key.Tag, key.Direction, n, n, nowUnix)
		if err != nil {
			return 0, 0, false, err
		}
		return uint64(n), uint64(n), false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}

	current := nonNegativeInt64(rawValue)
	previous := nonNegativeInt64(previousRaw)
	base := uint64(nonNegativeInt64(previousCumulative))

	delta, reset = xrayCounterDelta(previous, current)
	cumulative = base + delta

	_, err = s.db.ExecContext(ctx, `
UPDATE xray_counters
SET raw_value = ?, cumulative_value = ?, updated_at = ?
WHERE name = ?`, current, int64(cumulative), nowUnix, key.Name)
	if err != nil {
		return 0, 0, reset, err
	}
	return cumulative, delta, reset, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
