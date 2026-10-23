package transaction

import (
	"context"
	"errors"
	"sync"
	"time"
)

const driftValidationTimeout = 45 * time.Second

// DriftReconciler owns automatic validation for the application's lifetime.
// HTTP observations never own or cancel a validation process.
type DriftReconciler struct {
	engine      *Engine
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	done        chan struct{}
	running     bool
	checkedHash string
	result      error
}

func NewDriftReconciler(engine *Engine) *DriftReconciler {
	ctx, cancel := context.WithCancel(context.Background())
	return &DriftReconciler{engine: engine, ctx: ctx, cancel: cancel}
}

// Start detects startup drift after recovery, without delaying the normal API.
// The worker is owned by Close, rather than the bounded startup context.
func (r *DriftReconciler) Start(ctx context.Context) error {
	err := r.Observe(ctx)
	code, _, _ := ErrorInfo(err)
	if code == "config_validation_pending" {
		return nil
	}
	return err
}

// Observe starts at most one worker and returns immediately. A failed content
// hash is not repeatedly tested by polling; explicit Adopt can retry it.
func (r *DriftReconciler) Observe(ctx context.Context) error {
	metadata, err := r.engine.store.Metadata(ctx)
	if err != nil {
		return err
	}
	snapshot, err := readSnapshot(r.engine.configPath)
	if err != nil {
		return err
	}
	if snapshot.hash == metadata.ContentHash {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return r.ctx.Err()
	}
	if r.running {
		return driftPending()
	}
	if r.checkedHash == snapshot.hash {
		return r.result
	}
	r.done = make(chan struct{})
	r.running = true
	go r.run(snapshot.hash, r.done)
	return driftPending()
}

func driftPending() error {
	return publicError("config_validation_pending", "External Xray configuration is being validated", 409, nil)
}

func (r *DriftReconciler) run(hash string, done chan struct{}) {
	ctx, cancel := context.WithTimeout(r.ctx, driftValidationTimeout)
	err := r.engine.ReconcileDrift(ctx)
	cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = err
	r.checkedHash = ""
	// Infrastructure failures can be retried. Only actual config rejection is
	// remembered, and a different file hash always triggers a new check.
	if errors.Is(err, ErrValidationFailed) || errors.Is(err, ErrMarkupInvalid) {
		r.checkedHash = hash
	}
	r.running = false
	close(done)
}

func (r *DriftReconciler) Close() error {
	r.cancel()
	r.mu.Lock()
	done, running := r.done, r.running
	r.mu.Unlock()
	if running {
		<-done
	}
	return nil
}
