package coordinator

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

var ErrRecoveryBlocked = errors.New("mutations are blocked pending geodata recovery")

type coordinator struct {
	token   chan struct{}
	mu      sync.RWMutex
	blocked bool
	waiters int
}

type Lease struct {
	once        sync.Once
	maintenance bool
	active      atomic.Bool
}

var process = newCoordinator()

func newCoordinator() *coordinator {
	return &coordinator{token: make(chan struct{}, 1)}
}

func Acquire(ctx context.Context) (*Lease, error) {
	if ctx == nil {
		return nil, errors.New("mutation context is required")
	}
	process.mu.RLock()
	blocked := process.blocked
	process.mu.RUnlock()
	if blocked {
		return nil, ErrRecoveryBlocked
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case process.token <- struct{}{}:
	}
	process.mu.RLock()
	blocked = process.blocked
	process.mu.RUnlock()
	if blocked {
		<-process.token
		return nil, ErrRecoveryBlocked
	}
	return &Lease{}, nil
}

// Block prevents new ordinary mutations. Any mutation already admitted holds
// the token until completion; maintenance acquisition waits for it to finish.
func Block() {
	process.mu.Lock()
	process.blocked = true
	process.mu.Unlock()
}

func RecoveryBlocked() bool {
	process.mu.RLock()
	defer process.mu.RUnlock()
	return process.blocked
}

// AcquireMaintenance requires a previously blocked gate and bypasses only the
// ordinary-admission check while retaining exclusive mutation ownership.
func AcquireMaintenance(ctx context.Context) (*Lease, error) {
	if ctx == nil {
		return nil, errors.New("maintenance context is required")
	}
	process.mu.Lock()
	if !process.blocked {
		process.mu.Unlock()
		return nil, errors.New("maintenance lease requires recovery block")
	}
	process.waiters++
	process.mu.Unlock()
	select {
	case <-ctx.Done():
		process.mu.Lock()
		process.waiters--
		process.mu.Unlock()
		return nil, ctx.Err()
	case process.token <- struct{}{}:
	}
	process.mu.Lock()
	process.waiters--
	process.blocked = true
	process.mu.Unlock()
	lease := &Lease{maintenance: true}
	lease.active.Store(true)
	return lease, nil
}

func (lease *Lease) Unblock() error {
	if lease == nil || !lease.maintenance || !lease.active.Load() {
		return errors.New("only a recovery lease can unblock mutations")
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.waiters != 0 {
		return errors.New("another recovery is waiting for maintenance ownership")
	}
	process.blocked = false
	return nil
}

func (lease *Lease) Release() {
	lease.once.Do(func() {
		lease.active.Store(false)
		<-process.token
	})
}
