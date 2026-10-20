package loggingservice

import (
	"context"
	"errors"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/logpathcheck"
	"home-router/xray-core-helper/internal/transaction"
)

type PathChecker interface {
	Check(context.Context, bootstrap.LoggingConfig) (logpathcheck.Result, error)
}

// SetPathPolicy wires the read-only inspector before requests are served.
func (s *Service) SetPathPolicy(checker PathChecker) error {
	if checker == nil {
		return errors.New("logging path checker required")
	}
	s.checker = checker
	return nil
}

type confirmedTransaction interface {
	MutateConfirmedLogging(context.Context, uint64, transaction.Planner, bool, transaction.PrepareMutation, transaction.ConfirmedLoggingOptions) (transaction.Metadata, error)
}
