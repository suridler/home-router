package domainservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/domain"
)

var (
	errRuntimeIdentityUnavailable = errors.New("domain runtime identity unavailable")
	errRuntimeIdentityEmpty       = errors.New("domain runtime identity is empty")
	errRuntimeIdentityChanged     = errors.New("domain runtime identity changed during reconciliation")
)

// RuntimeReconciler synchronizes desired resources after an Xray restart or
// a failed runtime mutation. The last successful identity is intentionally kept
// in memory only: after a helper restart one replay is safe because the typed
// resource rule tags make replay idempotent without rebuilding static rules.
type RuntimeReconciler struct {
	service     *Service
	identity    func(context.Context) (string, error)
	lastToken   string
	BeforeApply func(context.Context, string) error
}

// NewRuntimeReconciler creates a runtime reconciler. Reconcile calls are
// serialized by the same process mutation coordinator used by Service.Apply,
// so lastToken needs no separate mutex.
func NewRuntimeReconciler(service *Service, identity func(context.Context) (string, error)) (*RuntimeReconciler, error) {
	if service == nil || identity == nil {
		return nil, errors.New("domain runtime reconciler requires service and identity")
	}
	return &RuntimeReconciler{service: service, identity: identity}, nil
}

// Reconcile synchronizes desired resources when the runtime identity changes
// or a previous mutation left runtime synchronization pending. It reports whether
// a runtime apply occurred. The token is recorded only after a successful
// apply and a stable second identity check, so any failure is retried by the
// next scheduled invocation.
func (reconciler *RuntimeReconciler) Reconcile(ctx context.Context) (bool, error) {
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer lease.Release()

	snapshot, err := reconciler.service.store.Snapshot(ctx)
	if err != nil {
		return false, fmt.Errorf("read proxy-domain state for runtime reconciliation: %w", err)
	}
	if snapshot.ConsolidationActive {
		return false, domain.ErrConsolidationActive
	}

	token, err := reconciler.identity(ctx)
	if err != nil {
		return false, errRuntimeIdentityUnavailable
	}
	if strings.TrimSpace(token) == "" {
		return false, errRuntimeIdentityEmpty
	}
	if token == reconciler.lastToken && !reconciler.service.runtimeDirty {
		return false, nil
	}

	if reconciler.BeforeApply != nil {
		if err := reconciler.BeforeApply(ctx, token); err != nil {
			return false, err
		}
	}

	if err := reconciler.service.ApplyWhileLocked(ctx); err != nil {
		return false, fmt.Errorf("apply desired proxy domains during runtime reconciliation: %w", err)
	}

	current, err := reconciler.identity(ctx)
	if err != nil {
		return false, errRuntimeIdentityUnavailable
	}
	if strings.TrimSpace(current) == "" {
		return false, errRuntimeIdentityEmpty
	}
	if current != token {
		return false, errRuntimeIdentityChanged
	}

	reconciler.lastToken = token
	return true, nil
}
