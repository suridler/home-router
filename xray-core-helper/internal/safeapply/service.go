package safeapply

import (
	"context"
	"errors"
	"sync"
	"time"

	"home-router/xray-core-helper/internal/templateapply"
	"home-router/xray-core-helper/internal/templatecatalog"
)

const DefaultConfirmationTimeout = 120 * time.Second

type Engine interface {
	PrepareConfirmed(context.Context, templatecatalog.ID, uint64) (templateapply.ConfirmedDescriptor, error)
	ActivateConfirmed(context.Context, templateapply.ConfirmedDescriptor) error
	Confirm(context.Context, templateapply.ConfirmedDescriptor) error
	RollbackConfirmed(context.Context, templateapply.ConfirmedDescriptor) error
	RecoverConfirmed(context.Context, templateapply.ConfirmedDescriptor) error
}
type Timer interface {
	Arm(context.Context) error
	Cancel(context.Context) error
}
type Service struct {
	mu      sync.Mutex
	store   *Store
	engine  Engine
	timer   Timer
	now     func() time.Time
	timeout time.Duration
	bootID  func() (string, error)
	retain  func() int
}

func NewService(store *Store, engine Engine, timer Timer, retention ...func() int) (*Service, error) {
	if store == nil || engine == nil || timer == nil {
		return nil, errors.New("safe apply dependencies are required")
	}
	retain := func() int { return 10 }
	if len(retention) > 1 || (len(retention) == 1 && retention[0] == nil) {
		return nil, errors.New("invalid safe apply retention")
	}
	if len(retention) == 1 {
		retain = retention[0]
	}
	return &Service{store: store, engine: engine, timer: timer, now: time.Now, timeout: DefaultConfirmationTimeout, bootID: currentBootID, retain: retain}, nil
}

func (service *Service) Begin(ctx context.Context, id templatecatalog.ID, generation uint64) (Transaction, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if pending, err := service.store.Pending(ctx); err != nil {
		return Transaction{}, err
	} else if pending != nil {
		return Transaction{}, ErrBusy
	}
	if err := service.store.pruneBaselines(ctx, service.retain()); err != nil {
		return Transaction{}, err
	}
	var baseline []BaselineFile
	if engine, ok := service.engine.(groupEngine); ok && systemMember(id) {
		var err error
		baseline, err = engine.CaptureSystemBaseline(ctx)
		if err != nil {
			return Transaction{}, err
		}
		if err = validateBaseline(baseline); err != nil {
			return Transaction{}, err
		}
	}
	descriptor, err := service.engine.PrepareConfirmed(ctx, id, generation)
	if err != nil {
		return Transaction{}, err
	}
	transaction, err := service.store.Begin(ctx, id, descriptor.BackupID, descriptor.CandidateHash, descriptor.Generation, service.now().Add(service.timeout))
	if err != nil {
		return Transaction{}, err
	}
	if baseline != nil {
		if err = service.store.RecordBaseline(ctx, transaction.ID, baseline); err != nil {
			// Nothing has been activated. Retain the pending evidence/gate rather
			// than guess what a failed durable write recorded.
			return Transaction{}, err
		}
		if err = service.verifyGroup(ctx, transaction); err != nil {
			return Transaction{}, err
		}
	}
	if err = service.timer.Arm(ctx); err != nil {
		if baseline != nil {
			// No activation has occurred; no file or live-state rollback is needed.
			_, _, resolveErr := service.store.Resolve(context.WithoutCancel(ctx), transaction.ID, StateRolledBack)
			if resolveErr != nil {
				return Transaction{}, resolveErr
			}
			return Transaction{}, err
		}
		_ = service.engine.RecoverConfirmed(context.WithoutCancel(ctx), descriptor)
		_, _, _ = service.store.Resolve(context.WithoutCancel(ctx), transaction.ID, StateRolledBack)
		return Transaction{}, err
	}
	if err = service.engine.ActivateConfirmed(ctx, descriptor); err != nil {
		if service.manualReboot(id) {
			// Activation can have succeeded before a later health check failed.
			// Conservatively restore the file and retain a reboot-required gate.
			if _, _, rollbackErr := service.rollbackLocked(context.WithoutCancel(ctx), transaction.ID); rollbackErr != nil {
				return Transaction{}, rollbackErr
			}
			return Transaction{}, err
		}
		if templateapply.IsCode(err, templateapply.CodeRollback) {
			// Keep the durable pending transaction and armed timer for recovery.
			return Transaction{}, err
		}
		_ = service.timer.Cancel(context.WithoutCancel(ctx))
		_, _, _ = service.store.Resolve(context.WithoutCancel(ctx), transaction.ID, StateRolledBack)
		return Transaction{}, err
	}
	return service.store.Advance(ctx, transaction.ID, PhasePrepared, PhaseApplied)
}
func (service *Service) Pending(ctx context.Context) (*Transaction, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.store.Pending(ctx)
}
func (service *Service) Confirm(ctx context.Context, id string) (Transaction, bool, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	pending, err := service.store.Pending(ctx)
	if err != nil {
		return Transaction{}, false, err
	}
	if pending == nil {
		return service.store.Resolve(ctx, id, StateConfirmed)
	}
	if pending.ID != id {
		return Transaction{}, false, ErrStale
	}
	if pending.Phase == PhaseAwaitingReboot {
		return *pending, false, ErrRebootRequired
	}
	if _, ok := service.engine.(groupEngine); ok && systemMember(pending.TemplateID) {
		if _, err := service.store.Baseline(ctx, pending.ID); err != nil {
			return *pending, false, ErrBaseline
		}
	}
	if pending.Phase == PhaseApplied {
		value, err := service.store.Advance(ctx, id, PhaseApplied, PhaseConfirming)
		if err != nil {
			return Transaction{}, false, err
		}
		pending = &value
	}
	if pending.Phase != PhaseConfirming {
		return Transaction{}, false, ErrPhase
	}
	if err = service.timer.Cancel(ctx); err != nil {
		return Transaction{}, false, err
	}
	if err = service.engine.Confirm(ctx, descriptor(*pending)); err != nil {
		return Transaction{}, false, err
	}
	return service.store.Resolve(ctx, id, StateConfirmed)
}
func (service *Service) Rollback(ctx context.Context, id string) (Transaction, bool, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.rollbackLocked(ctx, id)
}
func (service *Service) rollbackLocked(ctx context.Context, id string) (Transaction, bool, error) {
	pending, err := service.store.Pending(ctx)
	if err != nil {
		return Transaction{}, false, err
	}
	if pending == nil {
		return service.store.Resolve(ctx, id, StateRolledBack)
	}
	if pending.ID != id {
		return Transaction{}, false, ErrStale
	}
	if pending.Phase == PhaseAwaitingReboot {
		return *pending, false, nil
	}
	if pending.Phase == PhasePrepared || pending.Phase == PhaseApplied || pending.Phase == PhaseConfirming {
		value, err := service.store.Advance(ctx, id, pending.Phase, PhaseRollingBack)
		if err != nil {
			return Transaction{}, false, err
		}
		pending = &value
	}
	if pending.Phase != PhaseRollingBack {
		return Transaction{}, false, ErrPhase
	}
	grouped, err := service.restoreGroup(context.WithoutCancel(ctx), *pending)
	if err != nil {
		return *pending, false, err
	}
	if !grouped {
		err = service.engine.RollbackConfirmed(context.WithoutCancel(ctx), descriptor(*pending))
	}
	if err != nil {
		return *pending, false, err
	}
	if service.manualReboot(pending.TemplateID) {
		bootID, err := service.bootID() // only after every file has been restored
		if err != nil || !validBootID(bootID) {
			return *pending, false, ErrBootUnavailable
		}
		waiting, err := service.store.awaitReboot(context.WithoutCancel(ctx), id, bootID)
		if err != nil {
			return *pending, false, err
		}
		_ = service.timer.Cancel(context.WithoutCancel(ctx))
		return waiting, true, nil
	}
	_ = service.timer.Cancel(context.WithoutCancel(ctx))
	return service.store.Resolve(context.WithoutCancel(ctx), id, StateRolledBack)
}
func (service *Service) Recover(ctx context.Context) (*Transaction, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	pending, err := service.store.Pending(ctx)
	if err != nil || pending == nil {
		return pending, err
	}
	switch pending.Phase {
	case PhaseAwaitingReboot:
		previous, readErr := service.store.rebootBootID(ctx, pending.ID)
		if readErr != nil {
			return pending, readErr
		}
		current, readErr := service.bootID()
		if readErr != nil {
			return pending, readErr
		}
		if !validBootID(current) {
			return pending, ErrBootUnavailable
		}
		if current == previous {
			return pending, nil
		}
		if err := service.verifyGroup(ctx, *pending); err != nil {
			value, transitionErr := service.store.requireRestore(ctx, pending.ID)
			if transitionErr != nil {
				return pending, transitionErr
			}
			return &value, err
		}
		if engine, ok := service.engine.(interface {
			CompleteRebootRollback(context.Context, templateapply.ConfirmedDescriptor) error
		}); ok {
			if err := engine.CompleteRebootRollback(ctx, descriptor(*pending)); err != nil {
				return pending, err
			}
		}
		_, _, err = service.store.Resolve(ctx, pending.ID, StateRolledBack)
		return nil, err
	case PhaseConfirming:
		if _, ok := service.engine.(groupEngine); ok && systemMember(pending.TemplateID) {
			if _, err := service.store.Baseline(ctx, pending.ID); err != nil {
				return pending, ErrBaseline
			}
		}
		if err = service.engine.Confirm(ctx, descriptor(*pending)); err != nil {
			return pending, err
		}
		_, _, err = service.store.Resolve(ctx, pending.ID, StateConfirmed)
		_ = service.timer.Cancel(context.WithoutCancel(ctx))
		return nil, err
	case PhaseRollingBack, PhasePrepared, PhaseApplied:
		if pending.Phase != PhaseRollingBack {
			value, advanceErr := service.store.Advance(ctx, pending.ID, pending.Phase, PhaseRollingBack)
			if advanceErr != nil {
				return pending, advanceErr
			}
			pending = &value
		}
		grouped, groupErr := service.restoreGroup(ctx, *pending)
		if groupErr != nil {
			return pending, groupErr
		}
		if !grouped {
			err = service.engine.RecoverConfirmed(ctx, descriptor(*pending))
		}
		if err != nil {
			return pending, err
		}
		if service.manualReboot(pending.TemplateID) {
			bootID, bootErr := service.bootID()
			if bootErr != nil || !validBootID(bootID) {
				return pending, ErrBootUnavailable
			}
			waiting, waitErr := service.store.awaitReboot(ctx, pending.ID, bootID)
			if waitErr != nil {
				return pending, waitErr
			}
			_ = service.timer.Cancel(context.WithoutCancel(ctx))
			return &waiting, nil
		}
		_ = service.timer.Cancel(context.WithoutCancel(ctx))
		_, _, err = service.store.Resolve(ctx, pending.ID, StateRolledBack)
		return nil, err
	default:
		return pending, ErrPhase
	}
}
func descriptor(transaction Transaction) templateapply.ConfirmedDescriptor {
	return templateapply.ConfirmedDescriptor{TemplateID: transaction.TemplateID, BackupID: transaction.BackupID, CandidateHash: transaction.CandidateHash, Generation: transaction.Generation}
}
