package coordinator

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
)

func resetGate(t *testing.T) {
	t.Helper()
	Block()
	lease, err := AcquireMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Unblock(); err != nil {
		t.Fatal(err)
	}
	lease.Release()
	t.Cleanup(func() {
		Block()
		cleanup, err := AcquireMaintenance(context.Background())
		if err == nil {
			_ = cleanup.Unblock()
			cleanup.Release()
		}
	})
}

func TestAcquireRejectsWhileRecoveryBlockedAndMaintenanceCanUnblock(t *testing.T) {
	resetGate(t)
	Block()
	if !RecoveryBlocked() {
		t.Fatal("gate did not retain blocked state")
	}
	if _, err := Acquire(context.Background()); !errors.Is(err, ErrRecoveryBlocked) {
		t.Fatalf("Acquire() error = %v; want recovery-blocked", err)
	}
	lease, err := AcquireMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ordinary, err := Acquire(context.Background()); ordinary != nil || !errors.Is(err, ErrRecoveryBlocked) {
		t.Fatalf("ordinary Acquire() during maintenance = %v, %v", ordinary, err)
	}
	if err := lease.Unblock(); err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if RecoveryBlocked() {
		t.Fatal("gate remained blocked after maintenance unblocked it")
	}
}

func TestAcquireBlockUnblockRace(t *testing.T) {
	resetGate(t)
	const iterations = 100
	for range iterations {
		Block()
		if _, err := Acquire(context.Background()); !errors.Is(err, ErrRecoveryBlocked) {
			t.Fatalf("Acquire() after Block() = %v; want recovery-blocked", err)
		}
		lease, err := AcquireMaintenance(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			for range 8 {
				acquired, acquireErr := Acquire(context.Background())
				if acquireErr == nil {
					acquired.Release()
				}
			}
		}()
		go func() {
			defer workers.Done()
			_ = lease.Unblock()
			lease.Release()
		}()
		workers.Wait()
	}
}

func TestPendingRecoveryCannotBeOpenedByEarlierLease(t *testing.T) {
	resetGate(t)
	Block()
	first, err := AcquireMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan *Lease, 1)
	go func() {
		next, acquireErr := AcquireMaintenance(context.Background())
		if acquireErr != nil {
			acquired <- nil
			return
		}
		acquired <- next
	}()
	for {
		process.mu.RLock()
		waiters := process.waiters
		process.mu.RUnlock()
		if waiters != 0 {
			break
		}
		runtime.Gosched()
	}
	if err := first.Unblock(); err == nil || !RecoveryBlocked() {
		t.Fatalf("first recovery unblocked while another was waiting: err=%v blocked=%t",
			err, RecoveryBlocked())
	}
	first.Release()
	second := <-acquired
	if second == nil || !RecoveryBlocked() {
		t.Fatalf("second recovery lease = %v; blocked=%t", second, RecoveryBlocked())
	}
	if err := second.Unblock(); err != nil {
		t.Fatal(err)
	}
	second.Release()
}
