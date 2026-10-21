// Package errorhistory retains individual redacted error occurrences, never groups messages.
package errorhistory

import (
	"context"
	"database/sql"
	"errors"
	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/logreader"
	"home-router/xray-core-helper/internal/sqliteopen"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const Capacity = 100
const maxMessageBytes = 4096

var ErrUnavailable = errors.New("error history unavailable")

type Reader interface {
	ReadHistory(context.Context, time.Time) (logreader.Result, error)
}
type Service struct {
	mu     sync.Mutex
	db     *sql.DB
	reader Reader
	now    func() time.Time
}

var uris = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"<>]+`)
var sensitive = regexp.MustCompile(`(?i)(?:\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b|\b(?:password|passwd|private.?key|secret|token|short.?id|authorization|cookie|uuid|api.?key)\b)`)

func Open(ctx context.Context, path string, reader Reader) (*Service, error) {
	if !filepath.IsAbs(path) || reader == nil {
		return nil, ErrUnavailable
	}
	db, err := sqliteopen.Open(ctx, path, sqliteopen.Options{BusyTimeout: 5 * time.Second})
	if err != nil {
		return nil, ErrUnavailable
	}
	s := &Service{db: db, reader: reader, now: time.Now}
	_, err = db.ExecContext(ctx, ` CREATE TABLE IF NOT EXISTS xray_error_seen (seq INTEGER PRIMARY KEY, identity TEXT NOT NULL UNIQUE);
 CREATE TABLE IF NOT EXISTS xray_error_history (id INTEGER PRIMARY KEY, event_time TEXT NOT NULL, observed_at TEXT NOT NULL, source TEXT NOT NULL, message TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS xray_error_collection (singleton INTEGER PRIMARY KEY CHECK(singleton=1), collected_at TEXT NOT NULL, error_code TEXT NOT NULL, truncated INTEGER NOT NULL, source TEXT NOT NULL);
 INSERT OR IGNORE INTO xray_error_collection VALUES(1,'','',0,'');`)
	if err == nil {
		err = os.Chmod(path, 0600)
	}
	if err != nil {
		db.Close()
		return nil, ErrUnavailable
	}
	return s, nil
}
func (s *Service) Close() error { return s.db.Close() }

func errorEvent(e logreader.Event) bool {
	return logreader.IsError(e)
}
func redact(message string) string {
	// Log text can contain whole configs or multiline credentials. Omit a sensitive
	// entry entirely instead of relying on partial secret-value substitutions.
	message = uris.ReplaceAllString(message, "[redacted URI]")
	if sensitive.MatchString(message) {
		return "[Скрыта запись с потенциально секретными данными]"
	}
	message = strings.ToValidUTF8(message, "�")
	message = strings.Map(func(r rune) rune {
		if r < ' ' && r != '\t' {
			return ' '
		}
		return r
	}, message)
	if len(message) > maxMessageBytes {
		message = message[:maxMessageBytes]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
		message += "…"
	}
	return message
}

func (s *Service) Collect(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	started := s.now().UTC()
	var previous, source string
	if err := s.db.QueryRowContext(ctx, "SELECT collected_at,source FROM xray_error_collection WHERE singleton=1").Scan(&previous, &source); err != nil {
		return ErrUnavailable
	}
	since := time.Time{}
	if source == "journal" && previous != "" {
		if timestamp, err := time.Parse(time.RFC3339Nano, previous); err == nil {
			since = timestamp.Add(-time.Second)
		}
	}
	snapshot, err := s.reader.ReadHistory(ctx, since)
	if err != nil {
		_, _ = s.db.ExecContext(ctx, "UPDATE xray_error_collection SET error_code='log_collection_failed' WHERE singleton=1")
		return ErrUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	observed := s.now().UTC().Format(time.RFC3339Nano)
	for _, event := range snapshot.Events {
		if !errorEvent(event) || event.ID == "" {
			continue
		}
		result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO xray_error_seen(identity) VALUES(?)", event.ID)
		if err != nil {
			return ErrUnavailable
		}
		n, err := result.RowsAffected()
		if err != nil {
			return ErrUnavailable
		}
		if n == 0 {
			continue
		}
		id, err := result.LastInsertId()
		if err != nil {
			return ErrUnavailable
		}
		timestamp := ""
		if !event.Time.IsZero() {
			timestamp = event.Time.UTC().Format(time.RFC3339Nano)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO xray_error_history VALUES(?,?,?,?,?)", id, timestamp, observed, event.Source, redact(event.Message)); err != nil {
			return ErrUnavailable
		}
	}
	// Identities are a bounded replay checkpoint, not a second raw log archive.
	for _, query := range []string{`DELETE FROM xray_error_history WHERE id NOT IN (SELECT id FROM xray_error_history ORDER BY id DESC LIMIT 100)`, `DELETE FROM xray_error_seen WHERE seq NOT IN (SELECT seq FROM xray_error_seen ORDER BY seq DESC LIMIT 10000)`} {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return ErrUnavailable
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE xray_error_collection SET collected_at=?,error_code='',truncated=?,source=? WHERE singleton=1", started.Format(time.RFC3339Nano), snapshot.Truncated, string(snapshot.Source)); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *Service) List(ctx context.Context, limit int) (api.ErrorHistoryResponse, error) {
	if limit < 1 || limit > Capacity {
		return api.ErrorHistoryResponse{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	response := api.ErrorHistoryResponse{Errors: []api.ErrorEvent{}, Capacity: Capacity}
	if err := s.db.QueryRowContext(ctx, "SELECT collected_at,error_code,truncated,source FROM xray_error_collection WHERE singleton=1").Scan(&response.CollectedAt, &response.CollectionError, &response.Truncated, &response.Source); err != nil {
		return api.ErrorHistoryResponse{}, ErrUnavailable
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,event_time,observed_at,source,message FROM xray_error_history ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return api.ErrorHistoryResponse{}, ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var event api.ErrorEvent
		if rows.Scan(&event.ID, &event.Time, &event.ObservedAt, &event.Source, &event.Message) != nil {
			return api.ErrorHistoryResponse{}, ErrUnavailable
		}
		response.Errors = append(response.Errors, event)
	}
	if rows.Err() != nil {
		return api.ErrorHistoryResponse{}, ErrUnavailable
	}
	return response, nil
}
