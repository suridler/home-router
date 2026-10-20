package domainservice

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/xrayconfig"
)

const serviceConfig = `{
  // Kept exactly as written by the service.
  "log": {"loglevel":"warning"},
  "routing": {
    "unknownRoutingField": {"keep": true},
    "rules": [
      {"ruleTag":"ordinary"},
      {"ruleTag":"proxy-domains-permanent"}
    ]
  }
}`

type runtimeClient struct {
	mu       sync.Mutex
	payloads []xrayconfig.RoutingPayload
	fail     error
	active   int
	max      int
	entered  chan struct{}
	release  chan struct{}
}

func TestIPv4WithMissingPermanentRuleReportsPersistedStateWithoutEditingConfig(t *testing.T) {
	client := &runtimeClient{}
	service, store, path := newService(t, serviceConfig, client)
	err := service.Add(context.Background(), "192.0.2.1")
	var persisted *PossiblyPersistedError
	if !errors.As(err, &persisted) {
		t.Fatalf("missing permanent rule reported success: %v", err)
	}
	snapshot, _ := store.Snapshot(context.Background())
	if !reflect.DeepEqual(snapshot.Desired, []string{"192.0.2.1"}) {
		t.Fatal("persisted state was hidden")
	}
	got, _ := os.ReadFile(path)
	payloads, _ := client.recorded()
	if string(got) != serviceConfig || len(payloads) != 0 {
		t.Fatal("missing rule was silently repaired or applied")
	}
	if err := service.Delete(context.Background(), "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidStoredResourceStopsRuntimeApply(t *testing.T) {
	client := &runtimeClient{}
	service, store, _ := newService(t, serviceConfig, client)
	service.store = invalidResourceStore{DomainStore: store}
	if err := service.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "classify proxy resources") {
		t.Fatalf("invalid stored resource not rejected by classifier: %v", err)
	}
	payloads, _ := client.recorded()
	if len(payloads) != 0 {
		t.Fatal("invalid stored state reached runtime")
	}
}

type invalidResourceStore struct{ DomainStore }

func (s invalidResourceStore) Snapshot(context.Context) (domainstore.Snapshot, error) {
	return domainstore.Snapshot{Desired: []string{"999.0.0.1"}, Baseline: []string{}}, nil
}

func (client *runtimeClient) ApplyRouting(ctx context.Context, payload xrayconfig.RoutingPayload) error {
	client.mu.Lock()
	client.active++
	if client.active > client.max {
		client.max = client.active
	}
	client.payloads = append(client.payloads, append(xrayconfig.RoutingPayload(nil), payload...))
	fail := client.fail
	entered, release := client.entered, client.release
	if entered != nil {
		client.entered = nil
		close(entered)
	}
	client.mu.Unlock()
	if release != nil {
		select {
		case <-ctx.Done():
			client.mu.Lock()
			client.active--
			client.mu.Unlock()
			return ctx.Err()
		case <-release:
		}
	}
	client.mu.Lock()
	client.active--
	client.mu.Unlock()
	return fail
}

func (client *runtimeClient) recorded() ([]xrayconfig.RoutingPayload, int) {
	client.mu.Lock()
	defer client.mu.Unlock()
	payloads := make([]xrayconfig.RoutingPayload, len(client.payloads))
	for index, payload := range client.payloads {
		payloads[index] = append(xrayconfig.RoutingPayload(nil), payload...)
	}
	return payloads, client.max
}

func newService(t *testing.T, initial string, client RoutingClient) (*Service, *domainstore.Store, string) {
	t.Helper()
	store, err := domainstore.Open(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	reader := xrayconfig.NewReader(xrayconfig.Settings{Config: configPath})
	service, err := New(store, reader, client)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, configPath
}

func TestSnapshotAddDeleteAndIdempotentApply(t *testing.T) {
	client := &runtimeClient{}
	service, _, configPath := newService(t, serviceConfig, client)
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := service.Snapshot(context.Background())
	if err != nil || !reflect.DeepEqual(snapshot, domainstore.Snapshot{
		Desired: []string{}, Baseline: []string{},
	}) {
		t.Fatalf("Snapshot() = %#v, %v", snapshot, err)
	}
	for range 2 {
		if err := service.Add(context.Background(), "NEW.Example."); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = service.Snapshot(context.Background())
	if err != nil || !reflect.DeepEqual(snapshot.Desired, []string{"new.example"}) {
		t.Fatalf("Snapshot() after Add = %#v, %v", snapshot, err)
	}
	payloads, _ := client.recorded()
	if len(payloads) != 2 || !strings.Contains(string(payloads[0]), `"domain:new.example"`) ||
		!reflect.DeepEqual(payloads[0], payloads[1]) {
		t.Fatalf("Add payloads = %q; want identical additions overlay", payloads)
	}

	if err := service.Delete(context.Background(), "new.example"); err != nil {
		t.Fatal(err)
	}
	if err := service.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err = service.Snapshot(context.Background())
	if err != nil || len(snapshot.Desired) != 0 {
		t.Fatalf("Snapshot() after Delete = %#v, %v", snapshot, err)
	}
	payloads, _ = client.recorded()
	if len(payloads) != 5 || !reflect.DeepEqual(payloads[2], payloads[3]) ||
		!reflect.DeepEqual(payloads[3], payloads[4]) {
		t.Fatalf("delete/apply payloads = %q; want identical empty overlays", payloads[2:])
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("domain service modified Xray JSONC")
	}
}

func TestApplyWhileLockedUsesExistingCoordinatorLease(t *testing.T) {
	client := &runtimeClient{}
	service, _, _ := newService(t, serviceConfig, client)
	lease, err := coordinator.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err := service.ApplyWhileLocked(context.Background()); err != nil {
		t.Fatalf("ApplyWhileLocked() under the caller's lease = %v", err)
	}
}

func TestFailedRuntimeSyncReportsPossiblyPersistedMutationAndApplyRecovers(t *testing.T) {
	runtimeFailure := errors.New("runtime unavailable")
	client := &runtimeClient{fail: runtimeFailure}
	service, store, _ := newService(t, serviceConfig, client)
	err := service.Add(context.Background(), "saved.example")
	var persistedErr *PossiblyPersistedError
	if !errors.As(err, &persistedErr) {
		t.Fatalf("Add() error = %T %v; want PossiblyPersistedError", err, err)
	}
	if persistedErr.Code() != RuntimeSyncFailedCode || !persistedErr.PossiblyPersisted() {
		t.Fatalf("possibly persisted error = code %q, persisted %t", persistedErr.Code(), persistedErr.PossiblyPersisted())
	}
	if strings.Contains(err.Error(), runtimeFailure.Error()) {
		t.Fatalf("public error leaked runtime failure: %v", err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil || !reflect.DeepEqual(snapshot.Desired, []string{"saved.example"}) {
		t.Fatalf("desired state after runtime failure = %#v, %v", snapshot, err)
	}

	client.mu.Lock()
	client.fail = nil
	client.mu.Unlock()
	if err := service.Apply(context.Background()); err != nil {
		t.Fatalf("idempotent Apply() did not recover runtime: %v", err)
	}
	payloads, _ := client.recorded()
	if len(payloads) != 2 || !strings.Contains(string(payloads[1]), `"domain:saved.example"`) {
		t.Fatalf("retry payloads = %q; want persisted desired domain reapplied", payloads)
	}
}

type failingStore struct {
	DomainStore
	err error
}

func (store failingStore) AddDesired(context.Context, string) error { return store.err }

func TestPersistenceFailureDoesNotSynchronizeRuntime(t *testing.T) {
	persistErr := errors.New("store unavailable")
	client := &runtimeClient{}
	service, store, _ := newService(t, serviceConfig, client)
	service.store = failingStore{DomainStore: store, err: persistErr}
	if err := service.Add(context.Background(), "not-saved.example"); !errors.Is(err, persistErr) {
		t.Fatalf("Add() error = %v; want persistence error", err)
	}
	payloads, _ := client.recorded()
	if len(payloads) != 0 {
		t.Fatalf("runtime called after persistence failure: %q", payloads)
	}
}

func TestMutationSynchronizationFailuresAreTyped(t *testing.T) {
	tests := []struct {
		name                  string
		mutate                func(*Service) error
		expectedDesired       []string
		expectedRuntimeMatch  string
		forbiddenRuntimeMatch string
	}{
		{
			name: "add",
			mutate: func(service *Service) error {
				return service.Add(context.Background(), "added.example")
			},
			expectedDesired:      []string{"added.example"},
			expectedRuntimeMatch: `"domain:added.example"`,
		},
		{
			name: "delete",
			mutate: func(service *Service) error {
				return service.Delete(context.Background(), "removed.example")
			},
			expectedDesired:       []string{},
			forbiddenRuntimeMatch: `"domain:removed.example"`,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			client := &runtimeClient{fail: errors.New("unavailable")}
			service, store, _ := newService(t, serviceConfig, client)
			if testCase.name == "delete" {
				if err := store.AddDesired(context.Background(), "removed.example"); err != nil {
					t.Fatal(err)
				}
			}
			err := testCase.mutate(service)
			var persistedErr *PossiblyPersistedError
			if !errors.As(err, &persistedErr) || persistedErr.Code() != RuntimeSyncFailedCode ||
				!persistedErr.PossiblyPersisted() {
				t.Fatalf("mutation error = %#v; want typed possibly-persisted failure", err)
			}
			if err.Error() != "proxy-domain mutation persisted but runtime synchronization failed" {
				t.Fatalf("mutation error = %q; want stable persisted-state message", err)
			}
			snapshot, err := store.Snapshot(context.Background())
			if err != nil || !reflect.DeepEqual(snapshot.Desired, testCase.expectedDesired) {
				t.Fatalf("desired state after failed sync = %#v, %v", snapshot, err)
			}
			client.mu.Lock()
			client.fail = nil
			client.mu.Unlock()
			if err := service.Apply(context.Background()); err != nil {
				t.Fatalf("Apply() did not recover persisted %s: %v", testCase.name, err)
			}
			payloads, _ := client.recorded()
			if len(payloads) != 2 {
				t.Fatalf("runtime calls after retry = %d; want initial failure and retry", len(payloads))
			}
			retryPayload := string(payloads[1])
			if testCase.expectedRuntimeMatch != "" && !strings.Contains(retryPayload, testCase.expectedRuntimeMatch) {
				t.Fatalf("retry payload = %q; want domain match %q", retryPayload, testCase.expectedRuntimeMatch)
			}
			if testCase.forbiddenRuntimeMatch != "" && strings.Contains(retryPayload, testCase.forbiddenRuntimeMatch) {
				t.Fatalf("retry payload = %q; must not contain domain %q", retryPayload, testCase.forbiddenRuntimeMatch)
			}
		})
	}
}

func TestConcurrentMutationAndApplySerializeAndKeepLatestDesiredState(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	client := &runtimeClient{entered: entered, release: release}
	service, _, _ := newService(t, serviceConfig, client)
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	applyDone := make(chan error, 1)
	go func() { firstDone <- service.Add(context.Background(), "first.example") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first mutation did not reach runtime")
	}
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		secondDone <- service.Add(context.Background(), "second.example")
	}()
	applyStarted := make(chan struct{})
	go func() {
		close(applyStarted)
		applyDone <- service.Apply(context.Background())
	}()
	<-secondStarted
	<-applyStarted
	close(release)
	for name, done := range map[string]<-chan error{
		"first mutation":  firstDone,
		"second mutation": secondDone,
		"apply":           applyDone,
	} {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s mutation: %v", name, err)
			}
		case <-time.After(time.Second):
			t.Errorf("%s mutation did not complete", name)
		}
	}
	payloads, maxActive := client.recorded()
	if len(payloads) != 3 || maxActive != 1 {
		t.Fatalf("runtime calls = %d, max concurrent calls = %d; want 3 and 1", len(payloads), maxActive)
	}
	lastPayload := string(payloads[len(payloads)-1])
	if !strings.Contains(lastPayload, `"domain:first.example"`) ||
		!strings.Contains(lastPayload, `"domain:second.example"`) {
		t.Fatalf("last runtime payload = %s; want both persisted domains", lastPayload)
	}
}

type blockingValidator struct {
	entered chan struct{}
	release <-chan struct{}
}

func (validator blockingValidator) Validate(ctx context.Context, _ string) error {
	close(validator.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-validator.release:
		return nil
	}
}

func TestTransactionAndDomainMutationShareCoordinator(t *testing.T) {
	runtimeEntered := make(chan struct{})
	runtime := &runtimeClient{entered: runtimeEntered}
	service, _, configPath := newService(t, serviceConfig, runtime)

	validatorEntered := make(chan struct{})
	releaseValidator := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseValidator) }) }
	defer release()
	store, err := transaction.OpenStore(context.Background(), filepath.Join(t.TempDir(), "transaction.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	engine, err := transaction.NewEngine(configPath, store, blockingValidator{
		entered: validatorEntered,
		release: releaseValidator,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	transactionDone := make(chan error, 1)
	go func() {
		_, err := engine.Mutate(context.Background(), 0, func(source []byte) ([]transaction.ByteEdit, error) {
			offset := bytes.Index(source, []byte(`"warning"`))
			if offset < 0 {
				return nil, errors.New("test loglevel not found")
			}
			return []transaction.ByteEdit{{
				Start: offset, End: offset + len(`"warning"`), Replacement: []byte(`"debug"`),
			}}, nil
		}, false)
		transactionDone <- err
	}()
	select {
	case <-validatorEntered:
	case <-time.After(time.Second):
		t.Fatal("configuration transaction did not reach validation")
	}

	domainDone := make(chan error, 1)
	domainCtx, cancelDomain := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelDomain()
	go func() {
		domainDone <- service.Add(domainCtx, "shared-lock.example")
	}()
	select {
	case err := <-domainDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("domain mutation while configuration validation blocked = %v; want context deadline", err)
		}
	case <-runtimeEntered:
		t.Fatal("domain runtime synchronization began during configuration validation")
	case <-time.After(time.Second):
		t.Fatal("domain mutation did not return after its coordinator wait timed out")
	}

	release()
	select {
	case err := <-transactionDone:
		if err != nil {
			t.Fatalf("configuration transaction: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("configuration transaction did not complete")
	}
	if err := service.Add(context.Background(), "shared-lock.example"); err != nil {
		t.Fatalf("domain mutation after transaction: %v", err)
	}
	select {
	case <-runtimeEntered:
	default:
		t.Fatal("domain runtime synchronization did not run")
	}
}

func TestRuntimePayloadBuildFailureAlsoMarksMutationPossiblyPersisted(t *testing.T) {
	client := &runtimeClient{}
	service, store, _ := newService(t, `{"routing":{"rules":[]}}`, client)
	err := service.Add(context.Background(), "saved.example")
	var persistedErr *PossiblyPersistedError
	if !errors.As(err, &persistedErr) || persistedErr.Code() != RuntimeSyncFailedCode ||
		!persistedErr.PossiblyPersisted() {
		t.Fatalf("Add() error = %#v; want typed possibly-persisted failure", err)
	}
	snapshot, snapshotErr := store.Snapshot(context.Background())
	if snapshotErr != nil || !reflect.DeepEqual(snapshot.Desired, []string{"saved.example"}) {
		t.Fatalf("desired state = %#v, %v; want persisted mutation", snapshot, snapshotErr)
	}
	payloads, _ := client.recorded()
	if len(payloads) != 0 {
		t.Fatalf("runtime called with invalid routing payload: %q", payloads)
	}
}
