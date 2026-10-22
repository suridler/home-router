package transaction

import (
	"errors"
	"fmt"
)

var (
	ErrMarkupInvalid      = errors.New("Xray configuration helper annotations are invalid")
	ErrValidationFailed   = errors.New("Xray rejected the candidate configuration")
	ErrRestartFailed      = errors.New("Xray restart failed")
	ErrRollbackFailed     = errors.New("configuration rollback failed")
	ErrRecoveryRequired   = errors.New("configuration recovery is required")
	ErrRestartUnavailable = errors.New("restart hook is unavailable")
)

type PublicError struct {
	Code       string
	Message    string
	HTTPStatus int
	Cause      error
}

func (err *PublicError) Error() string { return err.Message }

func (err *PublicError) Unwrap() error { return err.Cause }

func ErrorInfo(err error) (code, message string, status int) {
	var publicErr *PublicError
	if errors.As(err, &publicErr) {
		return publicErr.Code, publicErr.Message, publicErr.HTTPStatus
	}
	switch {
	case errors.Is(err, ErrRollbackUnavailable):
		return "config_rollback_unavailable", "Previous accepted configuration is unavailable", 409
	case errors.Is(err, ErrStaleGeneration):
		return "generation_conflict", "expected configuration generation is stale", 409
	case errors.Is(err, ErrStateDrift):
		return "config_drift", "configuration differs from committed state; adopt it after validation", 409
	case errors.Is(err, ErrTransactionBusy):
		return "transaction_in_progress", "a configuration transaction is pending", 409
	case errors.Is(err, ErrMarkupInvalid):
		return "config_markup_invalid", "Xray configuration helper annotations are invalid", 422
	case errors.Is(err, ErrValidationFailed):
		return "validation_failed", "Xray rejected the candidate configuration", 422
	case errors.Is(err, ErrRestartFailed):
		return "restart_failed", "Xray restart failed; the previous configuration was restored", 503
	case errors.Is(err, ErrRestartUnavailable):
		return "restart_unavailable", "Xray restart is not configured", 503
	case errors.Is(err, ErrRollbackFailed):
		return "rollback_failed", "configuration rollback failed; recovery is required", 500
	case errors.Is(err, ErrRecoveryRequired):
		return "recovery_required", "configuration transaction recovery is required", 503
	default:
		return "transaction_failed", "configuration transaction failed", 500
	}
}

func publicError(code, message string, status int, cause error) error {
	return &PublicError{Code: code, Message: message, HTTPStatus: status, Cause: cause}
}

// Reject lets a planner refuse a mutation with a stable client-safe error.
func Reject(code, message string, status int) error {
	return &PublicError{Code: code, Message: message, HTTPStatus: status}
}

func wrapOperation(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
