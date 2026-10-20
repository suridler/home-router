package setup

import (
	"context"
	"errors"
	"sync"
)

var ErrRecoveryRequired = errors.New("setup recovery is required")

// Startup recovers setup before reading the configuration used to choose the
// daemon mode. A failed restore must never admit normal-mode initialization.
type Startup struct {
	mu         sync.Mutex
	store      SetupStore
	configPath string
	probe      ConfigProbe
	evidence   func(context.Context) error
}

func NewStartup(ctx context.Context, configPath string, store SetupStore, evidence ...func(context.Context) error) (*Startup, error) {
	if ctx == nil || configPath == "" || store == nil {
		return nil, errors.New("setup startup dependencies are required")
	}
	startup := &Startup{store: store, configPath: configPath}
	if len(evidence) > 0 {
		startup.evidence = evidence[0]
	}
	// Recovery errors are represented by a bounded state, so the daemon can
	// still expose health and explicit recovery without serving normal APIs.
	_ = startup.Recover(ctx)
	return startup, nil
}

func (s *Startup) Recover(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probe = ConfigProbe{State: ConfigUnavailable, SetupRequired: true, RecoveryRequired: true}
	if err := s.store.Recover(ctx); err != nil {
		return ErrRecoveryRequired
	}
	probe, err := ProbeConfig(s.configPath)
	if err != nil {
		return ErrRecoveryRequired
	}
	if operations, ok := s.store.(operationStore); ok {
		op, err := operations.LoadOperation(ctx)
		if err != nil {
			return ErrRecoveryRequired
		}
		if op != nil && op.Status == "configured_direct" && op.Phase == "completed" {
			if op.Version != 1 || probe.State != ConfigExisting || op.ConfigPath != s.configPath {
				return ErrRecoveryRequired
			}
			probe.SetupRequired, probe.RequiresChoice = false, false
		}
	}
	if probe.SetupRequired && s.evidence != nil && s.evidence(ctx) != nil {
		return ErrRecoveryRequired
	}
	s.probe = probe
	return nil
}

func (s *Startup) Status() ConfigProbe {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probe
}
