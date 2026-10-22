// Package xrayvalidation validates the configured active Xray configuration.
package xrayvalidation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/commandrunner"
	"home-router/xray-core-helper/internal/nativebinding"
)

const commandTimeout = 2 * time.Minute

type Code string

const (
	CodeInvalidConfig    Code = "invalid_config"
	CodeMarkupInvalid    Code = "config_markup_invalid"
	CodeCanceled         Code = "canceled"
	CodeTimedOut         Code = "timed_out"
	CodeValidationFailed Code = "validation_failed"
	CodeCommandFailed    Code = "command_failed"
)

// Error contains a stable code and no command details or output.
type Error struct {
	Code Code
}

func (failure *Error) Error() string {
	if failure == nil {
		return string(CodeCommandFailed)
	}
	return string(failure.Code)
}

type Status string

const StatusValid Status = "valid"

// Result is the safe projection of an active-config validation.
type Result struct {
	Status Status
}

// CommandRunner is the command boundary used by the service.
type CommandRunner interface {
	Run(context.Context, string, []string) error
}

type boundedRunner struct {
	executable string
	runner     *commandrunner.Runner
}

func (runner boundedRunner) Run(ctx context.Context, executable string, args []string) error {
	if executable != runner.executable {
		return &commandrunner.Error{Failure: commandrunner.FailureInvalidInput}
	}
	_, err := runner.runner.Run(ctx, args)
	return err
}

type Service struct {
	binary string
	config string
	runner CommandRunner
}

// New binds validation to the Xray binary and active config in bootstrap.
// A nil runner selects the bounded commandrunner implementation.
func New(config bootstrap.Config, runner CommandRunner) (*Service, error) {
	if !cleanAbsolutePath(config.Xray.Binary) || !cleanAbsolutePath(config.Xray.Config) {
		return nil, &Error{Code: CodeInvalidConfig}
	}
	if runner == nil {
		bounded, err := commandrunner.New(config.Xray.Binary, commandTimeout)
		if err != nil {
			return nil, &Error{Code: CodeInvalidConfig}
		}
		runner = boundedRunner{executable: config.Xray.Binary, runner: bounded}
	}
	return &Service{
		binary: config.Xray.Binary,
		config: config.Xray.Config,
		runner: runner,
	}, nil
}

// Validate checks only the configured active Xray config and returns no
// command output or config content.
func (service *Service) Validate(ctx context.Context) (Result, error) {
	if service == nil || service.runner == nil || ctx == nil {
		return Result{}, &Error{Code: CodeCommandFailed}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, contextFailure(err)
	}

	if _, err := nativebinding.ParseFile(service.config); err != nil {
		return Result{}, &Error{Code: CodeMarkupInvalid}
	}
	args := []string{"run", "-test", "-config", service.config}
	if err := service.runner.Run(ctx, service.binary, args); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, contextFailure(ctxErr)
		}
		return Result{}, commandFailure(err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, contextFailure(err)
	}
	return Result{Status: StatusValid}, nil
}

func cleanAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, '\x00')
}

func contextFailure(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimedOut}
	}
	return &Error{Code: CodeCanceled}
}

func commandFailure(err error) *Error {
	var runnerErr *commandrunner.Error
	if !errors.As(err, &runnerErr) {
		return &Error{Code: CodeCommandFailed}
	}
	switch runnerErr.Failure {
	case commandrunner.FailureCanceled:
		return &Error{Code: CodeCanceled}
	case commandrunner.FailureTimeout:
		return &Error{Code: CodeTimedOut}
	case commandrunner.FailureExit:
		return &Error{Code: CodeValidationFailed}
	default:
		return &Error{Code: CodeCommandFailed}
	}
}
