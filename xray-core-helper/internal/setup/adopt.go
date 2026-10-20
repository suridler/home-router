package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"home-router/xray-core-helper/internal/templatecatalog"
)

// Existing validates the active file without replacing it or restarting Xray.
func (s *fileStore) Existing(ctx context.Context, expected string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.entries {
		if entry.ID != templatecatalog.XrayConfig {
			continue
		}
		raw, _, err := readSafe(entry.SystemPath)
		if err != nil || expected == "" || contentHash(raw) != expected {
			return ErrRecoveryRequired
		}
		if err = s.validate(ctx, entry.ID, entry.SystemPath); err != nil {
			return &PreflightError{Code: "preflight_candidate_invalid"}
		}
		after, _, err := readSafe(entry.SystemPath)
		if err != nil || contentHash(after) != expected {
			return ErrRecoveryRequired
		}
		return nil
	}
	return errorsUnavailable
}

// Adopt records first onboarding of an existing Xray installation. It grants
// no permission to replace its config or activate network templates.
func (s *Service) Adopt(ctx context.Context, fingerprint string) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applying {
		return Result{}, ErrSetupBusy
	}
	if s.probe.RecoveryRequired {
		return Result{}, ErrRecoveryRequired
	}
	if !s.probe.SetupRequired {
		return Result{}, ErrSetupNotRequired
	}
	if s.probe.State != ConfigExisting || s.startup == nil {
		return Result{}, ErrSetupNotRequired
	}
	validator, ok := s.startup.store.(interface {
		Existing(context.Context, string) error
	})
	if !ok {
		return Result{}, errorsUnavailable
	}
	if err := validator.Existing(ctx, fingerprint); err != nil {
		return Result{}, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Result{}, errorsUnavailable
	}
	s.operation = &Operation{ID: hex.EncodeToString(id[:]), Status: "configured_direct", Phase: "completed", HandoffPending: true, UpdatedAt: time.Now().UTC()}
	if err := s.persist(ctx); err != nil {
		s.probe.RecoveryRequired = true
		return Result{}, ErrRecoveryRequired
	}
	s.probe = ConfigProbe{State: ConfigExisting}
	s.once.Do(func() { close(s.completed) })
	return Result{Status: "configured_direct", OperationID: s.operation.ID}, nil
}
