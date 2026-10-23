package transaction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func waitDrift(t *testing.T, r *DriftReconciler) {
	t.Helper()
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done == nil {
		t.Fatal("no drift worker started")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drift worker did not complete")
	}
}

func TestStartupDriftAdoptsWithoutStateRequests(t *testing.T) {
	engine, store, path := newTestEngine(t, transactionFixture, &fakeValidator{}, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	engine.validator = contextValidatorFunc(func(ctx context.Context, _ string) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > driftValidationTimeout {
			t.Error("worker has no bounded validation deadline")
		}
		close(entered)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	})
	external := []byte(transactionFixture + "\n// edited while helper stopped")
	if err := os.WriteFile(path, external, 0600); err != nil {
		t.Fatal(err)
	}
	r := NewDriftReconciler(engine)
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }); _ = r.Close() })
	startup, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(startup); err != nil {
		t.Fatal(err)
	}
	<-entered
	cancel()
	once.Do(func() { close(release) })
	waitDrift(t, r)
	metadata, err := store.Metadata(context.Background())
	if err != nil || metadata.ContentHash != hashBytes(external) || metadata.Generation != 1 {
		t.Fatalf("metadata=%#v error=%v", metadata, err)
	}
}

func TestHTTPObservationsShareWorkerAndDoNotCancelIt(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	validator := &fakeValidator{fn: func(string) error { close(entered); <-release; return nil }}
	engine, store, path := newTestEngine(t, transactionFixture, validator, nil)
	if err := os.WriteFile(path, []byte(transactionFixture+"\n// external"), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewDriftReconciler(engine)
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }); _ = r.Close() })
	request, cancel := context.WithCancel(context.Background())
	code, _, _ := ErrorInfo(r.Observe(request))
	if code != "config_validation_pending" {
		t.Fatal(code)
	}
	<-entered
	cancel()
	var group sync.WaitGroup
	for i := 0; i < 10; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			c, _, _ := ErrorInfo(r.Observe(context.Background()))
			if c != "config_validation_pending" {
				t.Errorf("unexpected %s", c)
			}
		}()
	}
	group.Wait()
	if validator.count() != 1 {
		t.Fatalf("parallel validation calls=%d", validator.count())
	}
	once.Do(func() { close(release) })
	waitDrift(t, r)
	metadata, _ := store.Metadata(context.Background())
	if metadata.Generation != 1 {
		t.Fatalf("request cancellation interrupted adoption: %#v", metadata)
	}
	if err := r.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if validator.count() != 1 {
		t.Fatal("adopted hash was checked again")
	}
}

func TestRejectedHashIsNotRetestedOnEveryPoll(t *testing.T) {
	validator := &fakeValidator{err: ErrValidationFailed}
	engine, store, path := newTestEngine(t, transactionFixture, validator, nil)
	external := []byte(transactionFixture + "\n// rejected")
	if err := os.WriteFile(path, external, 0600); err != nil {
		t.Fatal(err)
	}
	r := NewDriftReconciler(engine)
	t.Cleanup(func() { _ = r.Close() })
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitDrift(t, r)
	for i := 0; i < 3; i++ {
		if err := r.Observe(context.Background()); !errors.Is(err, ErrValidationFailed) {
			t.Fatalf("%v", err)
		}
	}
	if validator.count() != 1 {
		t.Fatal("polling repeats invalid config validation")
	}
	// Explicit Adopt can retry the same bytes after an external problem is fixed.
	validator.mu.Lock()
	validator.err = nil
	validator.mu.Unlock()
	if _, _, err := engine.Adopt(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if err := r.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(external, []byte("\n// changed again")...), 0600); err != nil {
		t.Fatal(err)
	}
	_ = r.Observe(context.Background())
	waitDrift(t, r)
	metadata, _ := store.Metadata(context.Background())
	if metadata.Generation != 2 || validator.count() != 3 {
		t.Fatalf("metadata=%#v calls=%d", metadata, validator.count())
	}
}

type contextValidatorFunc func(context.Context, string) error

func (f contextValidatorFunc) Validate(ctx context.Context, path string) error { return f(ctx, path) }

func TestClosingDriftWorkerCancelsValidationBeforeStoreClose(t *testing.T) {
	engine, store, path := newTestEngine(t, transactionFixture, &fakeValidator{}, nil)
	entered := make(chan struct{})
	engine.validator = contextValidatorFunc(func(ctx context.Context, _ string) error { close(entered); <-ctx.Done(); return ctx.Err() })
	if err := os.WriteFile(path, []byte(transactionFixture+"\n// external"), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewDriftReconciler(engine)
	_ = r.Observe(context.Background())
	<-entered
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	waitDrift(t, r)
	metadata, err := store.Metadata(context.Background())
	if err != nil || metadata.Generation != 0 {
		t.Fatalf("shutdown adopted canceled check: %#v %v", metadata, err)
	}
}

func TestValidationCancellationAndLaunchFailureAreNotConfigRejection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	validator, err := NewXrayValidator(filepath.Join(dir, "missing-xray"), dir)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := ErrorInfo(validator.Validate(context.Background(), path))
	if code != "validation_unavailable" {
		t.Fatal(code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = validator.Validate(ctx, path)
	code, _, _ = ErrorInfo(err)
	if code != "validation_cancelled" || errors.Is(err, ErrValidationFailed) {
		t.Fatalf("%s %v", code, err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	err = validator.Validate(ctx, path)
	code, _, _ = ErrorInfo(err)
	if code != "validation_timeout" || errors.Is(err, ErrValidationFailed) {
		t.Fatalf("%s %v", code, err)
	}
}
