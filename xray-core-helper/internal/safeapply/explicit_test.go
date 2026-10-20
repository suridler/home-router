package safeapply

import (
	"context"
	"errors"
	"testing"

	"home-router/xray-core-helper/internal/templateapply"
	"home-router/xray-core-helper/internal/templatecatalog"
)

func TestForceApplyConfirmsWithoutTimer(t *testing.T) {
	service, calls := serviceFixture(t)
	value, err := service.ApplyForce(context.Background(), templatecatalog.Nftables, 0)
	if err != nil || value.State != StateConfirmed {
		t.Fatal(value, err)
	}
	if got := len(*calls); got != 3 || (*calls)[0] != "prepare" || (*calls)[1] != "activate" || (*calls)[2] != "confirm" {
		t.Fatal("force used a timer or bypassed ordinary activation", *calls)
	}
	if pending, err := service.Pending(context.Background()); err != nil || pending != nil {
		t.Fatal(pending, err)
	}
}

func TestForceDoesNotOverridePending(t *testing.T) {
	ctx := context.Background()
	service, calls := serviceFixture(t)
	_, err := service.Begin(ctx, templatecatalog.Nftables, 0)
	if err != nil {
		t.Fatal(err)
	}
	before := len(*calls)
	if _, err = service.ApplyForce(ctx, templatecatalog.Nftables, 0); !errors.Is(err, ErrBusy) || len(*calls) != before {
		t.Fatal("force bypassed pending transaction", err, *calls)
	}
}

func TestForceActivationFailureRetainsRecoveryWithoutArmingTimer(t *testing.T) {
	ctx := context.Background()
	service, calls := serviceFixture(t)
	service.engine = rebootEngine{engineStub{calls: calls, fail: "activate"}}
	service.bootID = func() (string, error) { return firstBoot, nil }
	if _, err := service.ApplyForce(ctx, templatecatalog.Nftables, 0); err == nil {
		t.Fatal("failed activation reported success")
	}
	if contains(*calls, "arm") || contains(*calls, "confirm") {
		t.Fatal("failed force armed timer or confirmed", *calls)
	}
	if pending, err := service.Pending(ctx); err != nil || pending == nil || pending.Phase != PhaseAwaitingReboot {
		t.Fatal("failed force discarded recovery", pending, err)
	}
}

func TestCancelRebootIsExplicitDurableAndDoesNotChangeRuntime(t *testing.T) {
	ctx := context.Background()
	service, calls := serviceFixture(t)
	service.engine = rebootEngine{engineStub{calls: calls}}
	service.bootID = func() (string, error) { return firstBoot, nil }
	value, err := service.Begin(ctx, templatecatalog.Nftables, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.Rollback(ctx, value.ID); err != nil {
		t.Fatal(err)
	}
	before := len(*calls)
	resolved, changed, err := service.CancelReboot(ctx, value.ID)
	if err != nil || !changed || resolved.State != StateRebootCancelled {
		t.Fatal(resolved, changed, err)
	}
	effects := (*calls)[before:]
	if len(effects) != 2 || effects[0] != "cancel" || effects[1] != "reboot-complete" {
		t.Fatal("cancellation changed runtime", effects)
	}
	if pending, err := service.Recover(ctx); err != nil || pending != nil {
		t.Fatal("same boot reintroduced gate", pending, err)
	}
	history, err := service.store.byID(ctx, value.ID)
	if err != nil || history.State != StateRebootCancelled {
		t.Fatal("history lost", history, err)
	}
	before = len(*calls)
	if _, changed, err = service.CancelReboot(ctx, value.ID); err != nil || changed || len(*calls) != before {
		t.Fatal("cancellation not idempotent", changed, err)
	}
	if _, err = service.ApplyForce(ctx, templatecatalog.Nftables, 0); err != nil {
		t.Fatal("gate remained", err)
	}
}

func TestCancelRebootRejectsOtherPhasesAndStaleID(t *testing.T) {
	for _, phase := range []Phase{PhasePrepared, PhaseApplied, PhaseConfirming, PhaseRollingBack} {
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			service, calls := serviceFixture(t)
			value, err := service.Begin(ctx, templatecatalog.Nftables, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.store.db.Exec(`UPDATE safe_apply_transaction SET phase=? WHERE transaction_id=?`, phase, value.ID); err != nil {
				t.Fatal(err)
			}
			before := len(*calls)
			if _, _, err = service.CancelReboot(ctx, value.ID); !errors.Is(err, ErrPhase) || len(*calls) != before {
				t.Fatal("cancel allowed unfinished restoration", err, *calls)
			}
			if _, _, err = service.CancelReboot(ctx, "wrong-id"); !errors.Is(err, ErrStale) {
				t.Fatal(err)
			}
		})
	}
}

func TestCancelRebootMetadataFailureKeepsJournalForRetry(t *testing.T) {
	ctx := context.Background()
	service, calls := serviceFixture(t)
	service.engine = rebootEngine{engineStub{calls: calls}}
	service.bootID = func() (string, error) { return firstBoot, nil }
	value, err := service.Begin(ctx, templatecatalog.Nftables, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.Rollback(ctx, value.ID); err != nil {
		t.Fatal(err)
	}
	fail := func(context.Context, templateapply.ConfirmedDescriptor) error {
		return errors.New("metadata unavailable")
	}
	if _, _, err = CancelRebootRecovery(ctx, service.store, service.timer, value.ID, fail); err == nil {
		t.Fatal("metadata failure ignored")
	}
	if pending, _ := service.Pending(ctx); pending == nil || pending.Phase != PhaseAwaitingReboot {
		t.Fatal("failure cleared gate")
	}
	if value, changed, err := CancelRebootRecovery(ctx, service.store, service.timer, value.ID, nil); err != nil || !changed || value.State != StateRebootCancelled {
		t.Fatal(value, changed, err)
	}
}
