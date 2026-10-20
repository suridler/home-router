package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

var errorsUnavailable = errors.New("setup engine unavailable")

type Service struct {
	mu         sync.Mutex
	probe      ConfigProbe
	discovery  *Discoverer
	engine     *Engine
	startup    *Startup
	operations operationStore
	operation  *Operation
	applying   bool
	completed  chan struct{}
	once       sync.Once
}

func NewService(probe ConfigProbe, discovery *Discoverer, engine ...*Engine) *Service {
	var shared *Engine
	if len(engine) > 0 {
		shared = engine[0]
	}
	return &Service{probe: probe, discovery: discovery, engine: shared, completed: make(chan struct{})}
}
func NewStartupService(startup *Startup, discovery *Discoverer, engine *Engine) *Service {
	service := NewService(startup.Status(), discovery, engine)
	service.startup = startup
	if operations, ok := startup.store.(operationStore); ok {
		service.operations = operations
		if err := service.reloadOperation(context.Background()); err != nil {
			service.probe.RecoveryRequired = true
		}
	}
	if engine != nil {
		engine.progress = service.setPhase
	}
	return service
}

func (s *Service) Status() ConfigProbe {
	s.mu.Lock()
	defer s.mu.Unlock()
	probe := s.probe
	if s.operation != nil {
		op := *s.operation
		probe.Operation = &op
	}
	return probe
}
func (s *Service) Interfaces(ctx context.Context) ([]Interface, error) {
	return s.discovery.Discover(ctx)
}
func (s *Service) Plan(ctx context.Context, name string) (Plan, error) {
	return s.PlanAddress(ctx, name, "")
}
func (s *Service) PlanAddress(ctx context.Context, name, address string) (Plan, error) {
	return s.PlanWithConfig(ctx, name, address, false)
}
func (s *Service) PlanWithConfig(ctx context.Context, name, address string, replace bool) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applying {
		return Plan{}, ErrSetupBusy
	}
	if !s.probe.SetupRequired {
		return Plan{}, ErrSetupNotRequired
	}
	if s.probe.RecoveryRequired {
		return Plan{}, ErrRecoveryRequired
	}
	if s.engine == nil {
		return Plan{}, errorsUnavailable
	}
	return s.engine.PlanWithConfig(ctx, name, address, replace)
}
func (s *Service) Apply(ctx context.Context, plan Plan) (Result, error) {
	s.mu.Lock()
	if s.applying {
		s.mu.Unlock()
		return Result{}, ErrSetupBusy
	}
	if s.probe.RecoveryRequired {
		s.mu.Unlock()
		return Result{}, ErrRecoveryRequired
	}
	if !s.probe.SetupRequired {
		s.mu.Unlock()
		return Result{}, ErrSetupNotRequired
	}
	if s.engine == nil {
		s.mu.Unlock()
		return Result{}, errorsUnavailable
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		s.mu.Unlock()
		return Result{}, errorsUnavailable
	}
	s.operation = &Operation{ID: hex.EncodeToString(id[:]), Status: "applying", Phase: "preflight", UpdatedAt: time.Now().UTC()}
	if err := s.persist(ctx); err != nil {
		s.probe.RecoveryRequired = true
		s.mu.Unlock()
		return Result{}, ErrRecoveryRequired
	}
	s.applying = true
	s.mu.Unlock()
	// Losing an HTTP response must not cancel or repeat an installation.
	job, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	result, err := s.engine.Apply(job, plan)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applying = false
	if errors.Is(err, ErrRecoveryRequired) {
		s.probe.RecoveryRequired = true
	}
	if err == nil {
		s.probe = ConfigProbe{State: ConfigExisting}
		s.operation.NetworkInterface, s.operation.LANNetwork, s.operation.NetworkPrepared = plan.Interface, plan.LANNetwork, true
		s.operation.Status = "configured_direct"
		s.operation.Phase = "completed"
		s.operation.HandoffPending = true
	} else {
		s.operation.Status = "setup_required"
		s.operation.Phase = "failed"
		s.operation.ErrorCode = ErrorCode(err)
		if s.probe.RecoveryRequired {
			s.operation.Status = "recovery_required"
		}
	}
	s.operation.UpdatedAt = time.Now().UTC()
	result.OperationID = s.operation.ID
	if failure := s.persist(context.WithoutCancel(ctx)); failure != nil {
		s.probe.RecoveryRequired = true
		return Result{}, ErrRecoveryRequired
	}
	if err == nil {
		s.once.Do(func() { close(s.completed) })
	}
	return result, err
}
func (s *Service) Recover(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applying {
		return ErrSetupBusy
	}
	if s.startup != nil {
		err := s.startup.Recover(ctx)
		s.probe = s.startup.Status()
		if failure := s.reloadOperation(ctx); failure != nil {
			s.probe.RecoveryRequired = true
			err = ErrRecoveryRequired
		}
		if s.engine != nil {
			s.engine.setProbe(s.probe)
		}
		if err == nil && !s.probe.SetupRequired {
			s.once.Do(func() { close(s.completed) })
		}
		return err
	}
	if s.engine == nil {
		return errorsUnavailable
	}
	return s.engine.Recover(ctx)
}

func (s *Service) Completed() <-chan struct{} { return s.completed }
func (s *Service) HandoffPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.operation != nil && s.operation.HandoffPending && s.operation.Status == "configured_direct"
}
func (s *Service) CompleteHandoff(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.operation == nil {
		return nil
	}
	s.operation.HandoffPending = false
	s.operation.UpdatedAt = time.Now().UTC()
	if err := s.persist(ctx); err != nil {
		s.operation.HandoffPending = true
		return err
	}
	return nil
}
func (s *Service) persist(ctx context.Context) error {
	if s.operation != nil && s.startup != nil {
		s.operation.Version = 1
		s.operation.ConfigPath = s.startup.configPath
	}
	if s.operations != nil && s.operation != nil {
		return s.operations.SaveOperation(ctx, *s.operation)
	}
	return nil
}
func (s *Service) setPhase(phase string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.operation == nil {
		return errorsUnavailable
	}
	s.operation.Phase = phase
	s.operation.UpdatedAt = time.Now().UTC()
	return s.persist(context.Background())
}
func (s *Service) reloadOperation(ctx context.Context) error {
	if s.operations == nil {
		return nil
	}
	op, err := s.operations.LoadOperation(ctx)
	if err != nil {
		return err
	}
	s.operation = op
	if op == nil {
		return nil
	}
	if s.probe.RecoveryRequired {
		op.Status = "recovery_required"
		op.Phase = "failed"
		op.ErrorCode = "recovery_required"
	} else if op.Status == "applying" || op.Status == "recovery_required" {
		op.Status, op.Phase, op.ErrorCode = "setup_required", "interrupted", "setup_interrupted_rolled_back"
		op.HandoffPending = false
		s.probe.SetupRequired = true
	} else if op.Status == "configured_direct" && s.probe.SetupRequired {
		s.probe.RecoveryRequired = true
		return ErrRecoveryRequired
	}

	op.UpdatedAt = time.Now().UTC()
	return s.persist(ctx)
}
