package templateapply

import (
	"context"
	"testing"

	"home-router/xray-core-helper/internal/templatecatalog"
)

func TestConfirmedNftablesKeepsBootFileUntilConfirm(t *testing.T) {
	engine, calls := engineTestFixture(t, runtimeStub{}, replaceStub{})
	descriptor, err := engine.PrepareConfirmed(context.Background(), templatecatalog.Nftables, 2)
	if err != nil {
		t.Fatal(err)
	}
	if contains(*calls, "replace-prepare") || contains(*calls, "runtime-apply") {
		t.Fatalf("prepare changed system: %v", *calls)
	}
	if err = engine.ActivateConfirmed(context.Background(), descriptor); err != nil {
		t.Fatal(err)
	}
	if contains(*calls, "replace-prepare") {
		t.Fatalf("activation persisted nftables: %v", *calls)
	}
	if !contains(*calls, "runtime-apply") {
		t.Fatalf("runtime not loaded: %v", *calls)
	}
	if err = engine.Confirm(context.Background(), descriptor); err != nil {
		t.Fatal(err)
	}
	if !contains(*calls, "replace-prepare") || !contains(*calls, "state:completed") {
		t.Fatalf("confirm=%v", *calls)
	}
}
func TestConfirmedStartupRecoveryRestoresFileAndRuntime(t *testing.T) {
	engine, calls := engineTestFixture(t, runtimeStub{}, replaceStub{})
	descriptor, err := engine.PrepareConfirmed(context.Background(), templatecatalog.XrayConfig, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.RecoverConfirmed(context.Background(), descriptor); err != nil {
		t.Fatal(err)
	}
	if !contains(*calls, "restore-pending") || !contains(*calls, "runtime-rollback") || !contains(*calls, "state:rollback") {
		t.Fatalf("calls=%v", *calls)
	}
}
func TestConfirmedPersistentComponentRollbackUsesDurableBackup(t *testing.T) {
	engine, calls := engineTestFixture(t, runtimeStub{}, replaceStub{})
	descriptor, err := engine.PrepareConfirmed(context.Background(), templatecatalog.XrayConfig, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.ActivateConfirmed(context.Background(), descriptor); err != nil {
		t.Fatal(err)
	}
	if err = engine.RollbackConfirmed(context.Background(), descriptor); err != nil {
		t.Fatal(err)
	}
	if !contains(*calls, "restore-pending") || !contains(*calls, "runtime-rollback") || !contains(*calls, "state:rollback") {
		t.Fatalf("calls=%v", *calls)
	}
}

func TestNftRollbackRestoresOnlyFileAndPreservesPendingBackup(t *testing.T) {
	for _, recovering := range []bool{false, true} {
		engine, calls := engineTestFixture(t, runtimeStub{}, replaceStub{})
		descriptor, err := engine.PrepareConfirmed(context.Background(), templatecatalog.Nftables, 2)
		if err != nil {
			t.Fatal(err)
		}
		if recovering {
			err = engine.RecoverConfirmed(context.Background(), descriptor)
		} else {
			err = engine.RollbackConfirmed(context.Background(), descriptor)
		}
		if !IsCode(err, CodeSafeApply) || contains(*calls, "restore-pending") || contains(*calls, "runtime-rollback") || contains(*calls, "state:rollback") {
			t.Fatalf("group recovery must require manifest: %v %v", *calls, err)
		}
		if !engine.RollbackNeedsReboot(templatecatalog.Nftables) {
			t.Fatal("manual reboot policy missing")
		}
		if err = engine.CompleteRebootRollback(context.Background(), descriptor); err != nil || !contains(*calls, "state:rollback") {
			t.Fatal("reboot completion did not release backup", err)
		}
	}
}
