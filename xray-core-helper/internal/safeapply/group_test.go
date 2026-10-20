package safeapply

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"home-router/xray-core-helper/internal/templateapply"
	"home-router/xray-core-helper/internal/templatecatalog"
)

type groupedStub struct {
	engineStub
	files       []BaselineFile
	failCapture bool
	failRestore templatecatalog.ID
	failVerify  bool
	onRestore   func()
}

func (g *groupedStub) CaptureSystemBaseline(context.Context) ([]templateapply.BaselineFile, error) {
	*g.calls = append(*g.calls, "capture-group")
	if g.failCapture {
		return nil, ErrBaseline
	}
	return g.files, nil
}
func (g *groupedStub) RestoreSystemFile(_ context.Context, file templateapply.BaselineFile) error {
	*g.calls = append(*g.calls, "restore:"+string(file.TemplateID))
	if g.onRestore != nil {
		g.onRestore()
	}
	if file.TemplateID == g.failRestore {
		return fmt.Errorf("restore failed")
	}
	return nil
}
func (g *groupedStub) VerifySystemBaseline(context.Context, []templateapply.BaselineFile) error {
	if g.failVerify {
		return ErrBaseline
	}
	return nil
}
func (g *groupedStub) RollbackNeedsReboot(id templatecatalog.ID) bool { return systemMember(id) }
func (g *groupedStub) CompleteRebootRollback(context.Context, templateapply.ConfirmedDescriptor) error {
	*g.calls = append(*g.calls, "group-complete")
	return nil
}

func groupFixture(t *testing.T) (*Service, *groupedStub) {
	s, calls := serviceFixture(t)
	g := &groupedStub{engineStub: engineStub{calls: calls}, files: baselineFixture()}
	s.engine = g
	s.bootID = func() (string, error) { return firstBoot, nil }
	return s, g
}

func TestGroupCaptureFailureNeverPreparesOrActivates(t *testing.T) {
	s, g := groupFixture(t)
	g.failCapture = true
	if _, err := s.Begin(context.Background(), templatecatalog.Sysctl, 0); err == nil {
		t.Fatal("capture failure ignored")
	}
	if !reflect.DeepEqual(*g.calls, []string{"capture-group"}) {
		t.Fatal(*g.calls)
	}
	if pending, _ := s.Pending(context.Background()); pending != nil {
		t.Fatal("created operation without capture")
	}
}

func TestGroupRestoreFailureMatrixAndNewBoot(t *testing.T) {
	for _, failedFile := range baselineFixture() {
		failed := failedFile.TemplateID
		requestID := failed
		if !systemMember(requestID) {
			requestID = templatecatalog.TProxyUnit
		}
		t.Run(string(failed), func(t *testing.T) {
			s, g := groupFixture(t)
			ctx := context.Background()
			operation, err := s.Begin(ctx, requestID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if files, err := s.store.Baseline(ctx, operation.ID); err != nil || len(files) != 6 {
				t.Fatal("activation without complete manifest", err)
			}
			g.failRestore = failed
			if _, _, err = s.Rollback(ctx, operation.ID); err == nil {
				t.Fatal("restore failure ignored")
			}
			pending, _ := s.Pending(ctx)
			if pending == nil || pending.Phase != PhaseRollingBack {
				t.Fatal("partial restoration reported reboot-ready")
			}
			if _, err := s.store.rebootBootID(ctx, operation.ID); err == nil {
				t.Fatal("boot recorded before files restored")
			}
			var progress int
			if err := s.store.db.QueryRowContext(ctx, `SELECT count(*) FROM safe_apply_restore WHERE transaction_id=?`, operation.ID).Scan(&progress); err != nil {
				t.Fatal(err)
			}
			wantProgress := 0
			for _, file := range baselineFixture() {
				if file.TemplateID == failed {
					break
				}
				wantProgress++
			}
			if progress != wantProgress {
				t.Fatalf("durable restore progress=%d want=%d", progress, wantProgress)
			}
			// Simulate a boot in the middle of restoration. This boot cannot
			// count as the reboot after complete file restoration.
			s.bootID = func() (string, error) { return secondBoot, nil }
			g.failRestore = ""
			pending, err = s.Recover(ctx)
			if err != nil || pending == nil || pending.Phase != PhaseAwaitingReboot {
				t.Fatal("retry failed", err)
			}
			if pending, err = s.Recover(ctx); err != nil || pending == nil {
				t.Fatal("same boot completed recovery")
			}
			g.failVerify = true
			s.bootID = func() (string, error) { return "00000000-0000-0000-0000-000000000003", nil }
			if _, err = s.Recover(ctx); err == nil {
				t.Fatal("new boot ignored file mismatch")
			}
			g.failVerify = false
			if pending, err = s.Recover(ctx); err != nil || pending == nil || pending.Phase != PhaseAwaitingReboot {
				t.Fatal("drift recovery did not require another reboot", err)
			}
			s.bootID = func() (string, error) { return "00000000-0000-0000-0000-000000000004", nil }
			if pending, err = s.Recover(ctx); err != nil || pending != nil {
				t.Fatal("later valid boot did not complete", err)
			}
			for _, forbidden := range []string{"rollback", "recover"} {
				if contains(*g.calls, forbidden) {
					t.Fatal("component runtime rollback invoked")
				}
			}
			if !contains(*g.calls, "group-complete") {
				t.Fatal("not finalized")
			}
		})
	}
}

func TestGroupBootIdentityReadAfterRestoration(t *testing.T) {
	s, g := groupFixture(t)
	ctx := context.Background()
	op, err := s.Begin(ctx, templatecatalog.Nftables, 0)
	if err != nil {
		t.Fatal(err)
	}
	boot := firstBoot
	s.bootID = func() (string, error) { return boot, nil }
	g.onRestore = func() { boot = secondBoot }
	if _, _, err = s.Rollback(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if recorded, err := s.store.rebootBootID(ctx, op.ID); err != nil || recorded != secondBoot {
		t.Fatal("stale pre-restoration boot recorded")
	}
}

func TestGroupLegacyEvidenceFailsClosed(t *testing.T) {
	for _, phase := range []Phase{PhasePrepared, PhaseApplied, PhaseConfirming, PhaseRollingBack, PhaseAwaitingReboot} {
		t.Run(string(phase), func(t *testing.T) {
			s, g := groupFixture(t)
			ctx := context.Background()
			op := beginBaseline(t, s.store, templatecatalog.Nftables)
			if phase != PhasePrepared {
				op, _ = s.store.Advance(ctx, op.ID, PhasePrepared, PhaseApplied)
			}
			if phase == PhaseConfirming {
				op, _ = s.store.Advance(ctx, op.ID, PhaseApplied, PhaseConfirming)
			}
			if phase == PhaseRollingBack || phase == PhaseAwaitingReboot {
				op, _ = s.store.Advance(ctx, op.ID, PhaseApplied, PhaseRollingBack)
			}
			if phase == PhaseAwaitingReboot {
				op, _ = s.store.awaitReboot(ctx, op.ID, firstBoot)
				s.bootID = func() (string, error) { return secondBoot, nil }
			}
			if _, err := s.Recover(ctx); !errors.Is(err, ErrBaseline) {
				t.Fatalf("legacy baseline invented: %v", err)
			}
			if len(*g.calls) != 0 {
				t.Fatal("legacy record executed effects", *g.calls)
			}
			if pending, _ := s.Pending(ctx); pending == nil {
				t.Fatal("legacy evidence discarded")
			}
		})
	}
}

func TestInterruptedGroupConfirmationCanBeExplicitlyRolledBack(t *testing.T) {
	s, g := groupFixture(t)
	ctx := context.Background()
	op, err := s.Begin(ctx, templatecatalog.Sysctl, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Advance(ctx, op.ID, PhaseApplied, PhaseConfirming); err != nil {
		t.Fatal(err)
	}
	rolled, _, err := s.Rollback(ctx, op.ID)
	if err != nil || rolled.Phase != PhaseAwaitingReboot {
		t.Fatal("cannot recover interrupted confirmation", err)
	}
	if contains(*g.calls, "rollback") {
		t.Fatal("component live rollback invoked")
	}
}
