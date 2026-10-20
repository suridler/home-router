package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestDailyCalendarAndDST(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct{ now, want string }{
		{"2026-10-24T03:59:00+02:00", "2026-10-24T04:00:00+02:00"},
		{"2026-10-24T04:00:00+02:00", "2026-10-25T04:00:00+01:00"},
		{"2026-03-28T04:01:00+01:00", "2026-03-29T04:00:00+02:00"},
	} {
		now, _ := time.Parse(time.RFC3339, sample.now)
		got := Daily(4, 0)(now.In(zone)).Format(time.RFC3339)
		if got != sample.want {
			t.Fatal(got, sample.want)
		}
	}
}

func TestJobsDoNotOverlapAndStopOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	finished := make(chan struct{})
	var calls atomic.Int32
	join := Start(ctx, []Job{{Name: "blocked", Next: Every(time.Millisecond), Timeout: time.Hour, Run: func(ctx context.Context, _ time.Time) error {
		if calls.Add(1) != 1 {
			t.Error("overlapping invocation")
		}
		close(started)
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	}}}, nil)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("job did not start")
	}
	cancel()
	join()
	select {
	case <-finished:
	default:
		t.Fatal("join returned before job exited")
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

func TestFailuresRetryAndOnlyTransitionsAreReported(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reports []bool
	count := 0
	join := Start(ctx, []Job{{Name: "retry", Next: Every(time.Millisecond), Timeout: time.Second, Run: func(context.Context, time.Time) error {
		count++
		if count <= 2 {
			return errors.New("SECRET")
		}
		if count == 4 {
			cancel()
		}
		return nil
	}}}, func(name string, failed bool) {
		if name != "retry" {
			t.Error(name)
		}
		reports = append(reports, failed)
	})
	join()
	if !reflect.DeepEqual(reports, []bool{true, false}) {
		t.Fatal(reports)
	}
}

func TestTaskTimeoutDoesNotBlockOtherTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timeout := make(chan struct{})
	other := make(chan struct{})
	join := Start(ctx, []Job{
		{Name: "timeout", Next: Every(time.Millisecond), Timeout: 10 * time.Millisecond, Run: func(work context.Context, _ time.Time) error {
			<-work.Done()
			close(timeout)
			<-ctx.Done()
			return work.Err()
		}},
		{Name: "other", Next: Every(time.Millisecond), Timeout: time.Second, Run: func(ctx context.Context, _ time.Time) error { close(other); <-ctx.Done(); return ctx.Err() }},
	}, nil)
	select {
	case <-other:
	case <-time.After(3 * time.Second):
		t.Fatal("independent task blocked")
	}
	select {
	case <-timeout:
	case <-time.After(3 * time.Second):
		t.Fatal("missing timeout")
	}
	cancel()
	join()
}

func TestDailyDeadlinePersistsAndCoalescesMissedRuns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "helper.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	job := store.Persist(GeodataJob("unused", 4, 0), "geodata-04:00")
	due := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	got, err := job.Pending(ctx, due)
	if err != nil || !got.Equal(due) {
		t.Fatal(got, err)
	}
	store.Close()
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job = store.Persist(GeodataJob("unused", 4, 0), "geodata-04:00")
	got, err = job.Pending(ctx, time.Now().Add(time.Hour))
	if err != nil || !got.Equal(due) {
		t.Fatal("missed deadline lost", got, err)
	}
	next := job.Next(time.Now()).Truncate(time.Second)
	if err := job.Complete(ctx, next); err != nil {
		t.Fatal(err)
	}
	got, err = job.Pending(ctx, due)
	if err != nil || !got.Equal(next) {
		t.Fatal("deadline not advanced", got, err)
	}
}

func TestMissedDailyJobRunsOnceAndAdvancesDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	missed := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	calls := 0
	job := store.Persist(Job{Name: "daily", Next: Daily(4, 0), Timeout: time.Second, Run: func(_ context.Context, due time.Time) error {
		calls++
		if !due.Equal(missed) {
			t.Error("lost original deadline", due)
		}
		return nil
	}}, "daily")
	if _, err := job.Pending(ctx, missed); err != nil {
		t.Fatal(err)
	}
	complete := job.Complete
	job.Complete = func(ctx context.Context, next time.Time) error {
		err := complete(ctx, next)
		cancel()
		return err
	}
	join := Start(ctx, []Job{job}, func(string, bool) { t.Error("successful job logged") })
	join()
	if calls != 1 {
		t.Fatal(calls)
	}
	next, err := job.Pending(context.Background(), missed)
	if err != nil || !next.After(time.Now()) {
		t.Fatal("catch-up did not advance to future", next, err)
	}
}
