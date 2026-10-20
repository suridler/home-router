package safeapply

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/templateapply"
	"home-router/xray-core-helper/internal/templatecatalog"
)

type engineStub struct {
	calls *[]string
	fail  string
}

func (stub engineStub) PrepareConfirmed(context.Context, templatecatalog.ID, uint64) (templateapply.ConfirmedDescriptor, error) {
	*stub.calls = append(*stub.calls, "prepare")
	return templateapply.ConfirmedDescriptor{TemplateID: templatecatalog.Nftables, BackupID: "backup", CandidateHash: strings.Repeat("a", 64), Generation: 1}, nil
}
func (stub engineStub) ActivateConfirmed(context.Context, templateapply.ConfirmedDescriptor) error {
	*stub.calls = append(*stub.calls, "activate")
	if stub.fail == "activate" {
		return errors.New("failed")
	}
	return nil
}
func (stub engineStub) Confirm(context.Context, templateapply.ConfirmedDescriptor) error {
	*stub.calls = append(*stub.calls, "confirm")
	return nil
}
func (stub engineStub) RollbackConfirmed(context.Context, templateapply.ConfirmedDescriptor) error {
	*stub.calls = append(*stub.calls, "rollback")
	if stub.fail == "rollback" {
		return errors.New("failed")
	}
	return nil
}
func (stub engineStub) RecoverConfirmed(context.Context, templateapply.ConfirmedDescriptor) error {
	*stub.calls = append(*stub.calls, "recover")
	return nil
}

type timerStub struct{ calls *[]string }

func (stub timerStub) Arm(context.Context) error {
	*stub.calls = append(*stub.calls, "arm")
	return nil
}
func (stub timerStub) Cancel(context.Context) error {
	*stub.calls = append(*stub.calls, "cancel")
	return nil
}
func serviceFixture(t *testing.T) (*Service, *[]string) {
	t.Helper()
	calls := []string{}
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	store.now = func() time.Time { return time.Unix(100, 0) }
	t.Cleanup(func() { store.Close() })
	service, _ := NewService(store, engineStub{calls: &calls}, timerStub{calls: &calls})
	service.now = func() time.Time { return time.Unix(100, 0) }
	return service, &calls
}
func TestServiceBeginConfirmAndIdempotency(t *testing.T) {
	service, calls := serviceFixture(t)
	transaction, err := service.Begin(context.Background(), templatecatalog.Nftables, 0)
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Phase != PhaseApplied || !contains(*calls, "arm") {
		t.Fatalf("transaction=%#v calls=%v", transaction, *calls)
	}
	resolved, changed, err := service.Confirm(context.Background(), transaction.ID)
	if err != nil || !changed || resolved.State != StateConfirmed {
		t.Fatalf("resolved=%#v changed=%v err=%v", resolved, changed, err)
	}
	if _, changed, err = service.Confirm(context.Background(), transaction.ID); err != nil || changed {
		t.Fatalf("idempotent changed=%v err=%v", changed, err)
	}
	if !contains(*calls, "cancel") || !contains(*calls, "confirm") {
		t.Fatalf("calls=%v", *calls)
	}
}
func TestServiceTimeoutAndPreparedCrashRollback(t *testing.T) {
	service, calls := serviceFixture(t)
	transaction, err := service.Begin(context.Background(), templatecatalog.Nftables, 0)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return transaction.Deadline.Add(time.Second) }
	pending, err := service.Recover(context.Background())
	if err != nil || pending != nil || !contains(*calls, "recover") {
		t.Fatalf("pending=%#v calls=%v err=%v", pending, *calls, err)
	}
}
func TestServiceFailedRollbackRemainsPendingForRetry(t *testing.T) {
	calls := []string{}
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer store.Close()
	store.now = func() time.Time { return time.Unix(100, 0) }
	service, _ := NewService(store, engineStub{calls: &calls, fail: "rollback"}, timerStub{calls: &calls})
	service.now = func() time.Time { return time.Unix(100, 0) }
	transaction, err := service.Begin(context.Background(), templatecatalog.Nftables, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, changed, err := service.Rollback(context.Background(), transaction.ID)
	if err == nil || changed || got.Phase != PhaseRollingBack {
		t.Fatalf("transaction=%#v changed=%v err=%v", got, changed, err)
	}
	pending, pendingErr := service.Pending(context.Background())
	if pendingErr != nil || pending == nil || pending.ID != transaction.ID || pending.Phase != PhaseRollingBack {
		t.Fatalf("pending=%#v err=%v", pending, pendingErr)
	}
}
func TestServiceRejectsSecondPendingAndStaleID(t *testing.T) {
	service, _ := serviceFixture(t)
	transaction, err := service.Begin(context.Background(), templatecatalog.Nftables, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Begin(context.Background(), templatecatalog.Sysctl, 0); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy=%v", err)
	}
	if _, _, err = service.Rollback(context.Background(), "old-id"); !errors.Is(err, ErrStale) {
		t.Fatalf("stale=%v current=%s", err, transaction.ID)
	}
}
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type timerRunnerStub struct{ args [][]string }

func (stub *timerRunnerStub) Run(_ context.Context, args []string) error {
	stub.args = append(stub.args, append([]string(nil), args...))
	return nil
}
func TestSystemdTimerUsesOnlyFixedUnit(t *testing.T) {
	runner := &timerRunnerStub{}
	timer, _ := newSystemdTimer(runner)
	_ = timer.Arm(context.Background())
	_ = timer.Cancel(context.Background())
	if len(runner.args) != 2 || runner.args[0][0] != "restart" || runner.args[0][1] != rollbackTimerUnit || runner.args[1][0] != "stop" {
		t.Fatalf("args=%v", runner.args)
	}
}
