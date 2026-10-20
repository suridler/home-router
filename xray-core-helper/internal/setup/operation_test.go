package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type blockingStart struct{ entered, release chan struct{} }

func (s blockingStart) Start(context.Context) error { close(s.entered); <-s.release; return nil }
func TestOperationSurvivesDisconnectAndPreventsDuplicate(t *testing.T) {
	store, entries, _ := fileStoreFixture(t)
	startup, _ := NewStartup(context.Background(), entries[0].SystemPath, store)
	start := blockingStart{make(chan struct{}), make(chan struct{})}
	discovery := discoveryFake{[]Interface{{Name: "lan0", Addresses: []string{"10.0.0.1/24"}}}}
	engine, _ := NewEngine(startup.Status(), discovery, store, start)
	service := NewStartupService(startup, nil, engine)
	plan, _ := service.Plan(context.Background(), "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := service.Apply(ctx, plan); done <- err }()
	<-start.entered
	cancel()
	status := service.Status()
	if status.Operation == nil || status.Operation.Status != "applying" || status.Operation.Phase != "starting_xray" {
		t.Fatal(status)
	}
	id := status.Operation.ID
	if _, err := service.Apply(context.Background(), plan); !errors.Is(err, ErrSetupBusy) {
		t.Fatal(err)
	}
	reopened, err := store.LoadOperation(context.Background())
	if err != nil || reopened.ID != id {
		t.Fatal(reopened, err)
	}
	close(start.release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-service.Completed():
	case <-time.After(time.Second):
		t.Fatal("handoff not signaled")
	}
	if _, err = service.Apply(context.Background(), plan); !errors.Is(err, ErrSetupNotRequired) {
		t.Fatal(err)
	}
	startup, _ = NewStartup(context.Background(), entries[0].SystemPath, store)
	service = NewStartupService(startup, nil, nil)
	status = service.Status()
	if status.Operation.ID != id || status.Operation.Status != "configured_direct" || status.SetupRequired {
		t.Fatal(status)
	}
}
func TestInterruptedOperationKeepsIDAfterRecovery(t *testing.T) {
	store, entries, _ := fileStoreFixture(t)
	tx, err := store.Prepare(context.Background(), Plan{Interface: "lan0", LANNetwork: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	op := Operation{ID: "0123456789abcdef0123456789abcdef", Status: "applying", Phase: "committing", UpdatedAt: time.Now().UTC()}
	if err = store.SaveOperation(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	startup, _ := NewStartup(context.Background(), entries[0].SystemPath, store)
	service := NewStartupService(startup, nil, nil)
	status := service.Status()
	if status.Operation.ID != op.ID || status.Operation.ErrorCode != "setup_interrupted_rolled_back" || !status.SetupRequired {
		t.Fatal(status)
	}
}
func TestInvalidOperationBlocksNormalStartup(t *testing.T) {
	store, entries, _ := fileStoreFixture(t)
	os.MkdirAll(store.root, 0700)
	os.WriteFile(entries[0].SystemPath, []byte("existing"), 0600)
	os.WriteFile(filepath.Join(store.root, "operation.json"), []byte(`{"id":"secret"}`), 0600)
	startup, _ := NewStartup(context.Background(), entries[0].SystemPath, store)
	if !startup.Status().RecoveryRequired {
		t.Fatal("invalid operation admitted")
	}
	_ = store.validate(context.Background(), entries[0].ID, entries[0].WorkingPath)
}
