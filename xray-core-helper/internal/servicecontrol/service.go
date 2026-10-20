// Package servicecontrol provides allowlisted systemd service operations.
package servicecontrol

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/gatewaycheck"
	"strings"
	"time"

	"home-router/xray-core-helper/internal/commandrunner"
)

const (
	systemctlPath    = "/usr/bin/systemctl"
	commandTimeout   = 2 * time.Minute
	activeStateQuery = "ActiveState"
)

type Unit string

const (
	UnitXray   Unit = "xray.service"
	UnitTProxy Unit = "xray-tproxy-routing.service"
)

type Action string

const (
	ActionEnable  Action = "enable"
	ActionStatus  Action = "status"
	ActionStart   Action = "start"
	ActionStop    Action = "stop"
	ActionRestart Action = "restart"
)

type State string

const (
	StateActive       State = "active"
	StateReloading    State = "reloading"
	StateInactive     State = "inactive"
	StateFailed       State = "failed"
	StateActivating   State = "activating"
	StateDeactivating State = "deactivating"
	StateMaintenance  State = "maintenance"
	StateRefreshing   State = "refreshing"
)

type ErrorCode string

const (
	ErrorInvalidUnit   ErrorCode = "invalid_unit"
	ErrorInvalidAction ErrorCode = "invalid_action"
	ErrorCanceled      ErrorCode = "canceled"
	ErrorTimedOut      ErrorCode = "timed_out"
	ErrorCommandFailed ErrorCode = "command_failed"
	ErrorInvalidStatus ErrorCode = "invalid_status"
)

// Error contains only a stable code and never includes command output.
type Error struct {
	Code ErrorCode
}

func (failure *Error) Error() string {
	if failure == nil {
		return "service control failed"
	}
	return string(failure.Code)
}

type Result struct {
	Unit   Unit
	Action Action
	State  State
}

type Controller interface {
	Execute(context.Context, Unit, Action) (Result, error)
}

type commandRunner interface {
	Run(context.Context, []string) ([]byte, error)
}

type boundedRunner struct {
	runner *commandrunner.Runner
}

func (runner boundedRunner) Run(ctx context.Context, args []string) ([]byte, error) {
	result, err := runner.runner.Run(ctx, args)
	return result.Stdout(), err
}

type Service struct {
	gate   func(context.Context) error
	runner commandRunner
}

type XrayRestarter struct {
	services Controller
}

func NewXrayRestarter(services Controller) (*XrayRestarter, error) {
	if services == nil {
		return nil, errors.New("Xray service controller is required")
	}
	return &XrayRestarter{services: services}, nil
}

func (restarter *XrayRestarter) Restart(ctx context.Context) error {
	if restarter == nil || restarter.services == nil {
		return &Error{Code: ErrorCommandFailed}
	}
	_, err := restarter.services.Execute(ctx, UnitXray, ActionRestart)
	if err != nil {
		return &Error{Code: ErrorCommandFailed}
	}
	return nil
}

// New binds service control to the fixed systemctl executable and bounded
// command runner.
func New(configPaths ...string) (*Service, error) {
	runner, err := commandrunner.New(systemctlPath, commandTimeout)
	if err != nil {
		return nil, &Error{Code: ErrorCommandFailed}
	}
	return &Service{runner: boundedRunner{runner: runner}, gate: func(ctx context.Context) error { return gatewaycheck.Wait(ctx, configPaths...) }}, nil
}

// Execute performs one of the four supported actions on an allowlisted unit.
func (service *Service) Execute(ctx context.Context, unit Unit, action Action) (Result, error) {
	if !validUnit(unit) {
		return Result{}, &Error{Code: ErrorInvalidUnit}
	}
	if !validAction(action) || action == ActionEnable && unit != UnitTProxy {
		return Result{}, &Error{Code: ErrorInvalidAction}
	}
	if service == nil || service.runner == nil || ctx == nil {
		return Result{}, &Error{Code: ErrorCommandFailed}
	}
	if ctx.Err() != nil {
		return Result{}, &Error{Code: ErrorCanceled}
	}

	if service.gate != nil && unit == UnitTProxy && (action == ActionStart || action == ActionRestart || action == ActionEnable) {
		if service.gate(ctx) != nil {
			return Result{}, &Error{Code: ErrorCommandFailed}
		}
	}
	args := commandArgs(unit, action)
	stdout, err := service.runner.Run(ctx, args)
	if err != nil {
		return Result{}, commandFailure(err)
	}

	result := Result{Unit: unit, Action: action}
	if action == ActionStatus {
		state, ok := parseState(stdout)
		if !ok {
			return Result{}, &Error{Code: ErrorInvalidStatus}
		}
		result.State = state
	}
	return result, nil
}

func commandArgs(unit Unit, action Action) []string {
	if action == ActionStatus {
		return []string{"show", "--property=" + activeStateQuery, "--value", string(unit)}
	}
	return []string{string(action), string(unit)}
}

func validUnit(unit Unit) bool {
	return unit == UnitXray || unit == UnitTProxy
}

func validAction(action Action) bool {
	switch action {
	case ActionEnable, ActionStatus, ActionStart, ActionStop, ActionRestart:
		return true
	default:
		return false
	}
}

func parseState(output []byte) (State, bool) {
	state := State(strings.TrimSpace(string(output)))
	switch state {
	case StateActive, StateReloading, StateInactive, StateFailed, StateActivating,
		StateDeactivating, StateMaintenance, StateRefreshing:
		return state, true
	default:
		return "", false
	}
}

func commandFailure(err error) *Error {
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
