package domainservice

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/domain"
)

type identityFake struct {
	mu      sync.Mutex
	token   string
	pending []string
	failure error
	calls   int
}

func (fake *identityFake) Identity(context.Context) (string, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	if fake.failure != nil {
		return "", fake.failure
	}
	if len(fake.pending) > 0 {
		token := fake.pending[0]
		fake.pending = fake.pending[1:]
		return token, nil
	}
	return fake.token, nil
}

func (fake *identityFake) setToken(token string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.token = token
	fake.pending = nil
}

func (fake *identityFake) queue(tokens ...string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.pending = append([]string(nil), tokens...)
}

func (fake *identityFake) setFailure(err error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.failure = err
}

func (fake *identityFake) callCount() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.calls
}

func setRuntimeFailure(client *runtimeClient, err error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.fail = err
}

func TestNewRuntimeReconcilerRequiresServiceAndIdentity(t *testing.T) {
	service, _, _ := newService(t, serviceConfig, &runtimeClient{})
	identity := &identityFake{token: "runtime-a"}
	if reconciler, err := NewRuntimeReconciler(nil, identity.Identity); reconciler != nil || err == nil {
		t.Fatalf("NewRuntimeReconciler without service = %v, %v", reconciler, err)
	}
	if reconciler, err := NewRuntimeReconciler(service, nil); reconciler != nil || err == nil {
		t.Fatalf("NewRuntimeReconciler without identity = %v, %v", reconciler, err)
	}
}

func TestRuntimeReconcilerSkipsUnchangedIdentity(t *testing.T) {
	client := &runtimeClient{}
	service, store, _ := newService(t, serviceConfig, client)
	if err := store.AddDesired(context.Background(), "kept.example"); err != nil {
		t.Fatal(err)
	}
	identity := &identityFake{token: "runtime-a"}
	reconciler, err := NewRuntimeReconciler(service, identity.Identity)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := reconciler.Reconcile(context.Background())
	if err != nil || !changed {
		t.Fatalf("first Reconcile() = %t, %v; want applied", changed, err)
	}
	changed, err = reconciler.Reconcile(context.Background())
	if err != nil || changed {
		t.Fatalf("unchanged Reconcile() = %t, %v; want skipped", changed, err)
	}

	payloads, _ := client.recorded()
	if len(payloads) != 1 || !strings.Contains(string(payloads[0]), `"domain:kept.example"`) {
		t.Fatalf("runtime payloads = %q; want one desired-domain apply", payloads)
	}
	if calls := identity.callCount(); calls != 3 {
		t.Fatalf("identity calls = %d; want two checks for the apply and one for the skip", calls)
	}
}

func TestRuntimeReconcilerAppliesChangedIdentity(t *testing.T) {
	client := &runtimeClient{}
	service, store, _ := newService(t, serviceConfig, client)
	if err := store.AddDesired(context.Background(), "kept.example"); err != nil {
		t.Fatal(err)
	}
	identity := &identityFake{token: "runtime-a"}
	reconciler, err := NewRuntimeReconciler(service, identity.Identity)
	if err != nil {
		t.Fatal(err)
	}

	if changed, err := reconciler.Reconcile(context.Background()); err != nil || !changed {
		t.Fatalf("first Reconcile() = %t, %v; want applied", changed, err)
	}
	identity.setToken("runtime-b")
	if changed, err := reconciler.Reconcile(context.Background()); err != nil || !changed {
		t.Fatalf("changed identity Reconcile() = %t, %v; want applied", changed, err)
	}
	if changed, err := reconciler.Reconcile(context.Background()); err != nil || changed {
		t.Fatalf("repeat changed identity Reconcile() = %t, %v; want skipped", changed, err)
	}

	payloads, _ := client.recorded()
	if len(payloads) != 2 {
		t.Fatalf("runtime applies = %d; want one per identity", len(payloads))
	}
	for index, payload := range payloads {
		if !strings.Contains(string(payload), `"domain:kept.example"`) {
			t.Fatalf("runtime payload %d = %q; want desired domain", index, payload)
		}
	}
}

func TestRuntimeReconcilerRetriesFailedApply(t *testing.T) {
	applyFailure := errors.New("runtime unavailable")
	client := &runtimeClient{fail: applyFailure}
	service, _, _ := newService(t, serviceConfig, client)
	identity := &identityFake{token: "runtime-a"}
	reconciler, err := NewRuntimeReconciler(service, identity.Identity)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := reconciler.Reconcile(context.Background())
	if err == nil || changed {
		t.Fatalf("failed Reconcile() = %t, %v; want error without apply", changed, err)
	}
	setRuntimeFailure(client, nil)
	changed, err = reconciler.Reconcile(context.Background())
	if err != nil || !changed {
		t.Fatalf("retry Reconcile() = %t, %v; want applied", changed, err)
	}

	payloads, _ := client.recorded()
	if len(payloads) != 2 {
		t.Fatalf("runtime applies = %d; want failed attempt and retry", len(payloads))
	}
}

func TestRuntimeReconcilerRetriesWhenIdentityChangesDuringApply(t *testing.T) {
	client := &runtimeClient{}
	service, _, _ := newService(t, serviceConfig, client)
	identity := &identityFake{token: "runtime-a"}
	identity.queue("runtime-a", "runtime-b")
	reconciler, err := NewRuntimeReconciler(service, identity.Identity)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := reconciler.Reconcile(context.Background())
	if err == nil || changed {
		t.Fatalf("unstable identity Reconcile() = %t, %v; want error without apply record", changed, err)
	}
	if strings.Contains(err.Error(), "runtime-a") || strings.Contains(err.Error(), "runtime-b") {
		t.Fatalf("reconciliation error leaked identity: %v", err)
	}

	identity.setToken("runtime-a")
	changed, err = reconciler.Reconcile(context.Background())
	if err != nil || !changed {
		t.Fatalf("retry Reconcile() = %t, %v; want applied after unstable identity", changed, err)
	}
	payloads, _ := client.recorded()
	if len(payloads) != 2 {
		t.Fatalf("runtime applies = %d; want unstable attempt and retry", len(payloads))
	}
}

func TestRuntimeReconcilerRejectsEmptyIdentity(t *testing.T) {
	for _, token := range []string{"", "   "} {
		t.Run(strings.ReplaceAll(token, " ", "space"), func(t *testing.T) {
			client := &runtimeClient{}
			service, _, _ := newService(t, serviceConfig, client)
			identity := &identityFake{token: token}
			reconciler, err := NewRuntimeReconciler(service, identity.Identity)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := reconciler.Reconcile(context.Background())
			if err == nil || changed {
				t.Fatalf("empty identity Reconcile() = %t, %v; want failure", changed, err)
			}
			payloads, _ := client.recorded()
			if len(payloads) != 0 {
				t.Fatalf("runtime applies after empty identity = %d; want zero", len(payloads))
			}
		})
	}
}

func TestRuntimeReconcilerRedactsIdentityErrorsAndRetries(t *testing.T) {
	client := &runtimeClient{}
	service, _, _ := newService(t, serviceConfig, client)
	identity := &identityFake{token: "runtime-a", failure: errors.New("secret runtime identity")}
	reconciler, err := NewRuntimeReconciler(service, identity.Identity)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := reconciler.Reconcile(context.Background())
	if err == nil || changed {
		t.Fatalf("identity failure Reconcile() = %t, %v; want failure", changed, err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("identity failure error leaked details: %v", err)
	}
	if payloads, _ := client.recorded(); len(payloads) != 0 {
		t.Fatalf("runtime applied after identity failure: %d", len(payloads))
	}

	identity.setFailure(nil)
	if changed, err = reconciler.Reconcile(context.Background()); err != nil || !changed {
		t.Fatalf("retry Reconcile() = %t, %v; want applied", changed, err)
	}
}

func TestRuntimeReconcilerSkipsWhileConsolidationIsActive(t *testing.T) {
	client := &runtimeClient{}
	service, store, _ := newService(t, serviceConfig, client)
	if err := store.SetConsolidationActive(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	identity := &identityFake{token: "runtime-a"}
	reconciler, err := NewRuntimeReconciler(service, identity.Identity)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := reconciler.Reconcile(context.Background())
	if changed || !errors.Is(err, domain.ErrConsolidationActive) {
		t.Fatalf("consolidation Reconcile() = %t, %v; want ErrConsolidationActive", changed, err)
	}
	if calls := identity.callCount(); calls != 0 {
		t.Fatalf("identity calls while consolidation active = %d; want zero", calls)
	}
	if payloads, _ := client.recorded(); len(payloads) != 0 {
		t.Fatalf("runtime applies while consolidation active = %d; want zero", len(payloads))
	}
}

func TestRuntimeReconcilerConcurrentCallsAreSafe(t *testing.T) {
	const workers = 12
	client := &runtimeClient{}
	service, _, _ := newService(t, serviceConfig, client)
	identity := &identityFake{token: "runtime-a"}
	reconciler, err := NewRuntimeReconciler(service, identity.Identity)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		changed bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			<-start
			changed, err := reconciler.Reconcile(context.Background())
			results <- result{changed: changed, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	applied := 0
	for outcome := range results {
		if outcome.err != nil {
			t.Fatalf("concurrent Reconcile() error: %v", outcome.err)
		}
		if outcome.changed {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("concurrent applies = %d; want exactly one", applied)
	}
	payloads, maxActive := client.recorded()
	if len(payloads) != 1 || maxActive != 1 {
		t.Fatalf("runtime applies = %d, max concurrent = %d; want one serialized apply", len(payloads), maxActive)
	}
}

func TestRuntimeReconcilerHonorsCancelledContext(t *testing.T) {
	client := &runtimeClient{}
	service, _, _ := newService(t, serviceConfig, client)
	identity := &identityFake{token: "runtime-a"}
	reconciler, err := NewRuntimeReconciler(service, identity.Identity)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	changed, err := reconciler.Reconcile(ctx)
	if changed || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled Reconcile() = %t, %v; want deadline exceeded", changed, err)
	}
	if calls := identity.callCount(); calls != 0 {
		t.Fatalf("identity calls after cancellation = %d; want zero", calls)
	}
}
