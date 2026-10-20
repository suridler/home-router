package safeapply

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/templateapply"
	"home-router/xray-core-helper/internal/templatecatalog"
)

const firstBoot = "00000000-0000-0000-0000-000000000001"
const secondBoot = "00000000-0000-0000-0000-000000000002"

type rebootEngine struct{ engineStub }

func (stub rebootEngine) RollbackNeedsReboot(id templatecatalog.ID) bool {
	return id == templatecatalog.Nftables
}
func (stub rebootEngine) CompleteRebootRollback(context.Context, templateapply.ConfirmedDescriptor) error {
	*stub.calls = append(*stub.calls, "reboot-complete")
	return nil
}

func TestManualRebootStateSurvivesRestartAndClearsOnlyAfterBootChange(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, path)
	defer func() { store.Close() }()
	calls := []string{}
	boot := firstBoot
	makeService := func() *Service {
		store.now = func() time.Time { return time.Unix(100, 0) }
		service, err := NewService(store, rebootEngine{engineStub{calls: &calls}}, timerStub{calls: &calls})
		if err != nil {
			t.Fatal(err)
		}
		service.now = store.now
		service.bootID = func() (string, error) { return boot, nil }
		return service
	}
	service := makeService()
	transaction, err := service.Begin(ctx, templatecatalog.Nftables, 0)
	if err != nil {
		t.Fatal(err)
	}
	rolled, changed, err := service.Rollback(ctx, transaction.ID)
	if err != nil || !changed || rolled.Phase != PhaseAwaitingReboot || rolled.State != StatePending {
		t.Fatalf("premature rollback success: %#v %v", rolled, err)
	}
	if _, _, err = service.Confirm(ctx, transaction.ID); !errors.Is(err, ErrRebootRequired) {
		t.Fatal("confirmation allowed without reboot", err)
	}
	if _, err = service.Begin(ctx, templatecatalog.Sysctl, 0); !errors.Is(err, ErrBusy) {
		t.Fatal("new apply allowed before reboot")
	}
	before := len(calls)
	if _, changed, err = service.Rollback(ctx, transaction.ID); err != nil || changed || len(calls) != before {
		t.Fatal("repeat rollback reran effects")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTestStore(t, path)
	service = makeService()
	pending, err := service.Recover(ctx)
	if err != nil || pending == nil || pending.Phase != PhaseAwaitingReboot {
		t.Fatal("helper restart cleared reboot requirement", err)
	}
	if contains(calls, "reboot-complete") {
		t.Fatal("marked complete on same boot")
	}
	boot = secondBoot
	pending, err = service.Recover(ctx)
	if err != nil || pending != nil || !contains(calls, "reboot-complete") {
		t.Fatal("new boot did not finish recovery", err)
	}
	if pending, err = service.Pending(ctx); err != nil || pending != nil {
		t.Fatal("reboot gate remained after resolution")
	}
	resolved, err := store.byID(ctx, transaction.ID)
	if err != nil || resolved.State != StateRolledBack {
		t.Fatal("rollback not durably completed", err)
	}
}

func TestRebootMetadataFailureKeepsGate(t *testing.T) {
	service, calls := serviceFixture(t)
	service.engine = rebootEngine{engineStub{calls: calls}}
	service.bootID = func() (string, error) { return firstBoot, nil }
	transaction, err := service.Begin(context.Background(), templatecatalog.Nftables, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.Rollback(context.Background(), transaction.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.store.db.Exec(`DELETE FROM safe_apply_reboot`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Recover(context.Background()); err == nil {
		t.Fatal("missing evidence ignored")
	}
	pending, err := service.Pending(context.Background())
	if err != nil || pending == nil || pending.Phase != PhaseAwaitingReboot {
		t.Fatal("failure cleared recovery gate")
	}
}

func TestManualRebootActivationFailureRestoresFileAndStaysPending(t *testing.T) {
	service, calls := serviceFixture(t)
	service.engine = rebootEngine{engineStub{calls: calls, fail: "activate"}}
	service.bootID = func() (string, error) { return firstBoot, nil }
	if _, err := service.Begin(context.Background(), templatecatalog.Nftables, 0); err == nil {
		t.Fatal("activation failure hidden")
	}
	pending, err := service.Pending(context.Background())
	if err != nil || pending == nil || pending.Phase != PhaseAwaitingReboot || !contains(*calls, "rollback") {
		t.Fatal("failed activation discarded reboot requirement", err)
	}
}

func TestInterruptedRollbackRequiresARebootAfterFileRestore(t *testing.T) {
	service, calls := serviceFixture(t)
	service.engine = rebootEngine{engineStub{calls: calls}}
	service.bootID = func() (string, error) { return secondBoot, nil }
	transaction, err := service.Begin(context.Background(), templatecatalog.Nftables, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.store.Advance(context.Background(), transaction.ID, PhaseApplied, PhaseRollingBack); err != nil {
		t.Fatal(err)
	}
	pending, err := service.Recover(context.Background())
	if err != nil || pending == nil || pending.Phase != PhaseAwaitingReboot {
		t.Fatal("interrupted restore treated as completed", err)
	}
	if pending, err = service.Recover(context.Background()); err != nil || pending == nil {
		t.Fatal("restoration in this boot does not prove boot loader used restored file")
	}
}
