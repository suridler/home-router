package errorhistory

import (
	"context"
	"errors"
	"fmt"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/logreader"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeReader struct {
	events []logreader.Event
	err    error
	source logreader.Source
	since  time.Time
	onRead func()
}

func (f *fakeReader) ReadHistory(_ context.Context, since time.Time) (logreader.Result, error) {
	f.since = since
	if f.onRead != nil {
		f.onRead()
	}
	return logreader.Result{Events: f.events, Source: f.source}, f.err
}
func TestIndividualOccurrencesSurviveRestartWithoutAgeExpiryOrReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "helper.sqlite")
	reader := &fakeReader{}
	for i := range 120 {
		reader.events = append(reader.events, logreader.Event{ID: fmt.Sprint(i), Message: "[Error] identical message", Time: time.Unix(int64(i), 0), Source: "journal"})
	}
	s, err := Open(ctx, path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := s.List(ctx, 100)
	if err != nil || len(first.Errors) != 100 {
		t.Fatal(first, err)
	}
	if first.Errors[0].Time != time.Unix(119, 0).UTC().Format(time.RFC3339Nano) || first.Errors[99].Time != time.Unix(20, 0).UTC().Format(time.RFC3339Nano) {
		t.Fatal("wrong occurrence order")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	// Domain schema validation must tolerate these tables on the next daemon start.
	domains, err := domainstore.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	domains.Close()
	s, err = Open(ctx, path, reader)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.now = func() time.Time { return time.Date(2050, 1, 1, 0, 0, 0, 0, time.UTC) }
	if err = s.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	again, _ := s.List(ctx, 100)
	if len(again.Errors) != 100 || again.Errors[0] != first.Errors[0] {
		t.Fatal("replay duplicated or age expired", again)
	}
	reader.events = append(reader.events, logreader.Event{ID: "new", Message: "[Error] identical message", Time: time.Unix(120, 0), Source: "file"})
	if err = s.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	newest, _ := s.List(ctx, 100)
	if len(newest.Errors) != 100 || newest.Errors[1] != first.Errors[0] || newest.Errors[99].Time == first.Errors[99].Time {
		t.Fatal("wrong FIFO retention")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe database mode")
	}
}
func TestOnlyErrorsAndRedactionBeforePersistence(t *testing.T) {
	reader := &fakeReader{events: []logreader.Event{
		{ID: "info", Message: "[Info] failed to connect"}, {ID: "warn", Message: "[Warning] restarting"},
		{ID: "url", Message: `[Error] Post https://dns.example/query?token=secret-value: timeout`},
		{ID: "secret", Message: `[Error] privateKey="private-secret"`},
		{ID: "uuid", Message: `[Error] 12345678-1234-1234-1234-123456789012`},
		{ID: "long", Message: "[Error] " + strings.Repeat("ж", 5000)},
		{ID: "priority", Message: "service failed", Priority: "3"},
	}}
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), reader)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	history, _ := s.List(ctx, 100)
	if len(history.Errors) != 5 {
		t.Fatal(history)
	}
	rows, err := s.db.Query("SELECT message FROM xray_error_history")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var message string
		rows.Scan(&message)
		for _, secret := range []string{"secret-value", "private-secret", "12345678-1234", "dns.example"} {
			if strings.Contains(message, secret) {
				t.Fatal("secret persisted")
			}
		}
		if len(message) > maxMessageBytes+3 {
			t.Fatal("unbounded message")
		}
	}
}
func TestFailureAndCancellationPreserveHistory(t *testing.T) {
	ctx := context.Background()
	reader := &fakeReader{events: []logreader.Event{{ID: "one", Message: "[Error] first"}}}
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), reader)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	reader.err = errors.New("secret command output")
	if !errors.Is(s.Collect(ctx), ErrUnavailable) {
		t.Fatal("failure not surfaced")
	}
	snapshot, err := s.List(ctx, 5)
	if err != nil || len(snapshot.Errors) != 1 || snapshot.CollectionError != "log_collection_failed" {
		t.Fatal(snapshot, err)
	}
	reader.err = nil
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if s.Collect(cancelled) == nil {
		t.Fatal("cancelled collection succeeded")
	}
	snapshot, _ = s.List(ctx, 5)
	if len(snapshot.Errors) != 1 {
		t.Fatal("lost retained history")
	}
}

func TestJournalWatermarkStartsBeforeReadAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "helper.db")
	clock := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	start := clock
	reader := &fakeReader{source: logreader.SourceJournal, onRead: func() { clock = clock.Add(5 * time.Second) }}
	service, err := Open(ctx, path, reader)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return clock }
	if err = service.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	if !reader.since.IsZero() {
		t.Fatal("first read must bootstrap history")
	}
	service.Close()
	service, err = Open(ctx, path, reader)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	service.now = func() time.Time { return clock }
	if err = service.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	if !reader.since.Equal(start.Add(-time.Second)) {
		t.Fatal("watermark skipped concurrent writes", reader.since)
	}
	reader.source = logreader.SourceNone
	if err = service.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := service.List(ctx, 5)
	if snapshot.Source != "none" {
		t.Fatal("disabled log source hidden")
	}
	reader.source = logreader.SourceJournal
	if err = service.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	if !reader.since.IsZero() {
		t.Fatal("changed source must bootstrap")
	}
}
