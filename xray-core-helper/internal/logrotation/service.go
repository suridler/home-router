// Package logrotation controls only the logrotate installation owned by
// Xray-install. It does not implement log rotation itself.
package logrotation

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/commandrunner"
)

const (
	systemctlPath  = "/usr/bin/systemctl"
	timerUnit      = "logrotate@xray.timer"
	commandTimeout = 2 * time.Minute
)

var installationPaths = []string{
	"/etc/systemd/system/logrotate@.service",
	"/etc/systemd/system/logrotate@.timer",
	"/etc/logrotate.d/xray",
}

const timerTemplatePath = "/etc/systemd/system/logrotate@.timer"

type State string

const (
	StateEnabled   State = "enabled"
	StateDisabled  State = "disabled"
	StateUnchanged State = "unchanged"
)

type Result struct {
	State  State
	Forced bool
}

type ErrorCode string

const (
	ErrorMissingInstallation ErrorCode = "logrotate_not_installed"
	ErrorCanceled            ErrorCode = "canceled"
	ErrorTimedOut            ErrorCode = "timed_out"
	ErrorCommandFailed       ErrorCode = "command_failed"
)

type Error struct {
	Code ErrorCode
}

func (err *Error) Error() string {
	if err == nil {
		return "log rotation operation failed"
	}
	return string(err.Code)
}

type commandRunner interface {
	Run(context.Context, []string) ([]byte, error)
}

type pathChecker interface {
	ExistsRegular(string) bool
}

type boundedRunner struct {
	runner *commandrunner.Runner
}

func (runner boundedRunner) Run(ctx context.Context, args []string) ([]byte, error) {
	result, err := runner.runner.Run(ctx, args)
	return result.Stdout(), err
}

type osPathChecker struct{}

func (osPathChecker) ExistsRegular(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

type Service struct {
	runner commandRunner
	paths  pathChecker
}

type Snapshot interface {
	Commit() error
	Rollback() error
}

type snapshot struct {
	service  *Service
	previous bool
	changed  bool
}

func (snapshot *snapshot) Commit() error { return nil }

func (snapshot *snapshot) Rollback() error {
	if snapshot == nil || !snapshot.changed {
		return nil
	}
	return snapshot.service.setEnabled(context.Background(), snapshot.previous)
}

func New() (*Service, error) {
	runner, err := commandrunner.New(systemctlPath, commandTimeout)
	if err != nil {
		return nil, &Error{Code: ErrorCommandFailed}
	}
	return &Service{runner: boundedRunner{runner: runner}, paths: osPathChecker{}}, nil
}

// Apply enforces the requested timer state. An omitted rotation request leaves
// an installed timer unchanged, except that a configuration with no file-backed
// logs always disables the timer. A missing installation is tolerated only for
// that forced-disable case because there is then nothing to manage.
func (service *Service) Apply(ctx context.Context, request api.LogConfigurationRequest) (Result, error) {
	if service == nil || service.runner == nil || service.paths == nil {
		return Result{}, &Error{Code: ErrorCommandFailed}
	}
	if ctx == nil || ctx.Err() != nil {
		return Result{}, &Error{Code: ErrorCanceled}
	}

	fileBacked := request.Access.Mode == api.LogTargetFile || request.Error.Mode == api.LogTargetFile
	if !fileBacked {
		if !service.paths.ExistsRegular(timerTemplatePath) {
			return Result{State: StateDisabled, Forced: true}, nil
		}
		if err := service.setEnabled(ctx, false); err != nil {
			return Result{}, err
		}
		return Result{State: StateDisabled, Forced: true}, nil
	}
	if request.Rotation == nil {
		return Result{State: StateUnchanged}, nil
	}
	if !service.installed() {
		return Result{}, &Error{Code: ErrorMissingInstallation}
	}
	if err := service.setEnabled(ctx, request.Rotation.Enabled); err != nil {
		return Result{}, err
	}
	if request.Rotation.Enabled {
		return Result{State: StateEnabled}, nil
	}
	return Result{State: StateDisabled}, nil
}

// Prepare applies the required fixed timer state and returns an in-process
// snapshot that restores the previous enabled state if the enclosing config
// transaction fails.
func (service *Service) Prepare(ctx context.Context, request api.LogConfigurationRequest) (Snapshot, error) {
	if service == nil || service.runner == nil || service.paths == nil {
		return nil, &Error{Code: ErrorCommandFailed}
	}
	if ctx == nil || ctx.Err() != nil {
		return nil, &Error{Code: ErrorCanceled}
	}
	fileBacked := request.Access.Mode == api.LogTargetFile || request.Error.Mode == api.LogTargetFile
	var desired *bool
	if !fileBacked {
		value := false
		desired = &value
		if !service.paths.ExistsRegular(timerTemplatePath) {
			return &snapshot{}, nil
		}
	} else if request.Rotation != nil {
		value := request.Rotation.Enabled
		desired = &value
		if !service.installed() {
			return nil, &Error{Code: ErrorMissingInstallation}
		}
	} else {
		return &snapshot{}, nil
	}

	previous, err := service.enabled(ctx)
	if err != nil {
		return nil, err
	}
	if previous == *desired {
		return &snapshot{}, nil
	}
	if err := service.setEnabled(ctx, *desired); err != nil {
		return nil, err
	}
	return &snapshot{service: service, previous: previous, changed: true}, nil
}

func (service *Service) installed() bool {
	for _, path := range installationPaths {
		if !service.paths.ExistsRegular(path) {
			return false
		}
	}
	return true
}

func (service *Service) setEnabled(ctx context.Context, enabled bool) error {
	action := "disable"
	if enabled {
		action = "enable"
	}
	if _, err := service.runner.Run(ctx, []string{action, "--now", timerUnit}); err != nil {
		var commandError *commandrunner.Error
		if errors.As(err, &commandError) {
			switch commandError.Failure {
			case commandrunner.FailureCanceled:
				return &Error{Code: ErrorCanceled}
			case commandrunner.FailureTimeout:
				return &Error{Code: ErrorTimedOut}
			}
		}
		return &Error{Code: ErrorCommandFailed}
	}
	return nil
}

func (service *Service) enabled(ctx context.Context) (bool, error) {
	output, err := service.runner.Run(ctx, []string{"show", "--property=UnitFileState", "--value", timerUnit})
	if err != nil {
		return false, commandError(err)
	}
	switch strings.TrimSpace(string(output)) {
	case "enabled", "enabled-runtime":
		return true, nil
	case "disabled":
		return false, nil
	default:
		return false, &Error{Code: ErrorCommandFailed}
	}
}

func commandError(err error) error {
	var runnerError *commandrunner.Error
	if errors.As(err, &runnerError) {
		switch runnerError.Failure {
		case commandrunner.FailureCanceled:
			return &Error{Code: ErrorCanceled}
		case commandrunner.FailureTimeout:
			return &Error{Code: ErrorTimedOut}
		}
	}
	return &Error{Code: ErrorCommandFailed}
}
