package domainservice

import (
	"context"
	"errors"
	"fmt"

	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/domain"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/xrayconfig"
)

const RuntimeSyncFailedCode = "domain_runtime_sync_failed"

type DomainStore interface {
	Snapshot(context.Context) (domainstore.Snapshot, error)
	AddDesired(context.Context, string) error
	DeleteDesired(context.Context, string) error
}

type RoutingReader interface {
	RoutingPayload(context.Context, domain.Overlay) (xrayconfig.RoutingPayload, error)
}

type RoutingClient interface {
	ApplyRouting(context.Context, xrayconfig.RoutingPayload) error
}

type Service struct {
	store  DomainStore
	reader RoutingReader
	client RoutingClient
}

func New(store DomainStore, reader RoutingReader, client RoutingClient) (*Service, error) {
	if store == nil || reader == nil || client == nil {
		return nil, errors.New("domain store, routing reader and routing client are required")
	}
	return &Service{store: store, reader: reader, client: client}, nil
}

type PossiblyPersistedError struct {
	cause error
}

func (err *PossiblyPersistedError) Error() string {
	return "proxy-domain mutation persisted but runtime synchronization failed"
}

func (err *PossiblyPersistedError) Unwrap() error { return err.cause }

func (err *PossiblyPersistedError) Code() string { return RuntimeSyncFailedCode }

func (err *PossiblyPersistedError) PossiblyPersisted() bool { return true }

func (service *Service) Snapshot(ctx context.Context) (domainstore.Snapshot, error) {
	return service.store.Snapshot(ctx)
}

func (service *Service) Add(ctx context.Context, name string) error {
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	if err := service.store.AddDesired(ctx, name); err != nil {
		return fmt.Errorf("persist desired proxy domain: %w", err)
	}
	if err := service.apply(ctx); err != nil {
		return &PossiblyPersistedError{cause: err}
	}
	return nil
}

func (service *Service) Delete(ctx context.Context, name string) error {
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	if err := service.store.DeleteDesired(ctx, name); err != nil {
		return fmt.Errorf("persist desired proxy-domain deletion: %w", err)
	}
	if err := service.apply(ctx); err != nil {
		return &PossiblyPersistedError{cause: err}
	}
	return nil
}

func (service *Service) Apply(ctx context.Context) error {
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	return service.ApplyWhileLocked(ctx)
}

// ApplyWhileLocked synchronizes the current desired domains when the caller
// already owns the process mutation coordinator.
func (service *Service) ApplyWhileLocked(ctx context.Context) error {
	if ctx == nil {
		return errors.New("domain apply context is required")
	}
	if err := service.apply(ctx); err != nil {
		return fmt.Errorf("apply desired proxy domains: %w", err)
	}
	return nil
}

func (service *Service) apply(ctx context.Context) error {
	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("read desired proxy domains: %w", err)
	}
	overlay, err := domain.BuildResourceOverlay(snapshot.Desired, snapshot.Baseline)
	if err != nil {
		return fmt.Errorf("classify proxy resources: %w", err)
	}
	payload, err := service.reader.RoutingPayload(ctx, overlay)
	if err != nil {
		return fmt.Errorf("build proxy-domain routing payload: %w", err)
	}
	if err := service.client.ApplyRouting(ctx, payload); err != nil {
		return fmt.Errorf("synchronize proxy-domain runtime: %w", err)
	}
	return nil
}
