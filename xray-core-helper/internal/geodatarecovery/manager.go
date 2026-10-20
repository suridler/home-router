package geodatarecovery

import (
	"context"
	"sync"

	"home-router/xray-core-helper/internal/consolidation"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/geodatainstall"
)

type StandardRecovery interface {
	RollbackWhileLocked(context.Context) error
	CleanupOrphansWhileLocked(context.Context) error
}

type CustomRecovery interface {
	RollbackWhileLocked(context.Context) error
	FinalizeWhileLocked(context.Context) error
	CleanupPartialWhileLocked(context.Context) error
	RecoverIncompletePrepareWhileLocked(context.Context) error
}

type ManagerOptions struct {
	Inspector *Inspector
	Standard  StandardRecovery
	Custom    CustomRecovery
}

type Status struct {
	Result           Result `json:"result"`
	Code             string `json:"code"`
	MutationsBlocked bool   `json:"mutationsBlocked"`
}

type Manager struct {
	inspector *Inspector
	standard  StandardRecovery
	custom    CustomRecovery
	mu        sync.RWMutex
	recoverMu sync.Mutex
	status    Status
}

func NewManager(options ManagerOptions) (*Manager, error) {
	if options.Inspector == nil || options.Standard == nil || options.Custom == nil {
		return nil, errRecoveryDependencies
	}
	return &Manager{
		inspector: options.Inspector, standard: options.Standard, custom: options.Custom,
		status: blockedStatus(ActionRetryInspection),
	}, nil
}

var errRecoveryDependencies = recoveryError("geodata recovery dependencies are required")

type recoveryError string

func (err recoveryError) Error() string { return string(err) }

func (manager *Manager) Status() Status {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.status
}

// Recover blocks ordinary mutation admission before acquiring exclusive
// maintenance ownership. Every failure retains a redacted status and gate.
func (manager *Manager) Recover(ctx context.Context) Status {
	manager.recoverMu.Lock()
	defer manager.recoverMu.Unlock()
	coordinator.Block()
	manager.setStatus(blockedStatus(ActionRetryInspection))
	lease, err := coordinator.AcquireMaintenance(ctx)
	if err != nil {
		return manager.setStatus(blockedStatus(ActionRetryInspection))
	}
	defer lease.Release()

	result, _, custom, _, inspectErr := manager.inspector.inspect(ctx)
	if inspectErr != nil {
		return manager.setStatus(blockedStatus(ActionRetryInspection))
	}
	if result.State.Phase == PhaseClean {
		if err := lease.Unblock(); err != nil {
			return manager.setStatus(blockedStatus(ActionRetryRecovery))
		}
		return manager.setStatus(Status{Result: result, Code: RecoveryNone.String()})
	}
	if !result.Plan.AutomaticRecovery || result.Plan.Recovery == RecoveryNone {
		action := result.Plan.OperatorAction
		if action == "" {
			action = ActionReviewEvidence
		}
		return manager.setStatus(Status{Result: result, Code: action, MutationsBlocked: true})
	}
	if err := manager.execute(ctx, result.Plan.Recovery, custom); err != nil {
		failed := result
		failed.Plan.AutomaticRecovery = false
		failed.Plan.OperatorAction = ActionRetryRecovery
		return manager.setStatus(Status{Result: failed, Code: ActionRetryRecovery, MutationsBlocked: true})
	}
	after, _, _, _, inspectErr := manager.inspector.inspect(ctx)
	if inspectErr != nil {
		return manager.setStatus(blockedStatus(ActionRetryInspection))
	}
	if after.State.Phase != PhaseClean {
		action := after.Plan.OperatorAction
		if action == "" {
			action = ActionRetryRecovery
		}
		return manager.setStatus(Status{Result: after, Code: action, MutationsBlocked: true})
	}
	if err := lease.Unblock(); err != nil {
		return manager.setStatus(blockedStatus(ActionRetryRecovery))
	}
	return manager.setStatus(Status{Result: after, Code: RecoveryNone.String()})
}

func (manager *Manager) execute(ctx context.Context, action Recovery, custom consolidation.Inspection) error {
	switch action {
	case RecoveryNone:
		return errRecoveryDependencies
	case RecoveryClearCustomFreeze:
		return manager.custom.RecoverIncompletePrepareWhileLocked(ctx)
	case RecoveryRollbackStandard:
		return manager.standard.RollbackWhileLocked(ctx)
	case RecoveryCleanupStandard:
		return manager.standard.CleanupOrphansWhileLocked(ctx)
	case RecoveryRollbackCustom:
		return manager.custom.RollbackWhileLocked(ctx)
	case RecoveryCleanupCustom:
		return manager.cleanupCustom(ctx, custom)
	case RecoveryRollbackBoth:
		if err := manager.standard.RollbackWhileLocked(ctx); err != nil {
			return err
		}
		return manager.custom.RollbackWhileLocked(ctx)
	case RecoveryCleanupBoth:
		if err := manager.standard.CleanupOrphansWhileLocked(ctx); err != nil {
			return err
		}
		return manager.cleanupCustom(ctx, custom)
	default:
		return errRecoveryDependencies
	}
}

func (manager *Manager) cleanupCustom(ctx context.Context, inspection consolidation.Inspection) error {
	switch {
	case inspection.Phase == consolidation.InspectionManifest && inspection.FinalizedEvidence:
		return manager.custom.FinalizeWhileLocked(ctx)
	case inspection.Phase == consolidation.InspectionPartial:
		return manager.custom.CleanupPartialWhileLocked(ctx)
	default:
		return errRecoveryDependencies
	}
}

func (manager *Manager) setStatus(status Status) Status {
	manager.mu.Lock()
	manager.status = status
	manager.mu.Unlock()
	return status
}

func blockedStatus(code string) Status {
	result := result(PhaseInspectionFailed, RecoveryNone, code)
	return Status{Result: result, Code: code, MutationsBlocked: true}
}

func UnavailableStatus() Status {
	return blockedStatus(ActionRetryInspection)
}

var _ StandardInspector = (*geodatainstall.Installer)(nil)
var _ CustomInspector = (*consolidation.Service)(nil)
var _ DomainSnapshotter = (*domainstore.Store)(nil)
