package safeapply

import (
	"context"
	"home-router/xray-core-helper/internal/templateapply"
)

// CancelRebootRecovery is shared by the daemon and explicit offline recovery.
// The caller must hold the daemon's mutation/runtime lock. Only journal and
// backup metadata change; no runtime, file restore or Xray discovery is involved.
func CancelRebootRecovery(ctx context.Context, store *Store, timer Timer, id string, complete func(context.Context, templateapply.ConfirmedDescriptor) error) (Transaction, bool, error) {
	pending, err := store.Pending(ctx)
	if err != nil {
		return Transaction{}, false, err
	}
	if pending == nil {
		return store.Resolve(ctx, id, StateRebootCancelled)
	}
	if pending.ID != id {
		return Transaction{}, false, ErrStale
	}
	if pending.Phase != PhaseAwaitingReboot {
		return Transaction{}, false, ErrPhase
	}
	if err = timer.Cancel(ctx); err != nil {
		return Transaction{}, false, err
	}
	if complete != nil {
		if err = complete(ctx, descriptor(*pending)); err != nil {
			return Transaction{}, false, err
		}
	}
	return store.Resolve(ctx, id, StateRebootCancelled)
}
