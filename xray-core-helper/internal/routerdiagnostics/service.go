// Package routerdiagnostics reports observations and explicitly declared checks.
package routerdiagnostics

import (
	"context"
	"errors"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/commandrunner"
	"home-router/xray-core-helper/internal/nativebinding"
)

const (
	ipPath         = "/sbin/ip"
	systemctlPath  = "/usr/bin/systemctl"
	commandTimeout = 10 * time.Second
)

type CheckName string

const (
	CheckDefaultRoute     CheckName = "default_route"
	CheckXrayService      CheckName = "xray_service_active"
	CheckTProxyService    CheckName = "tproxy_service_active"
	CheckDeclaredBindings CheckName = "declared_bindings"
	CheckNativeEffects    CheckName = "native_effects"
)

type CheckStatus string

const (
	StatusOK          CheckStatus = "ok"
	StatusMismatch    CheckStatus = "mismatch"
	StatusUnavailable CheckStatus = "unavailable"
	StatusFailed      CheckStatus = "failed"
	StatusCanceled    CheckStatus = "canceled"
	StatusTimedOut    CheckStatus = "timed_out"
	StatusUnchecked   CheckStatus = "unchecked"
	StatusNotDeclared CheckStatus = "not_declared"
)

type Check struct {
	Code       string
	Name       CheckName
	Status     CheckStatus
	TemplateID string
	BindingID  string
}
type Result struct{ Checks []Check }
type ErrorCode string

const ErrorInvalidService ErrorCode = "invalid_service"

type Error struct{ Code ErrorCode }

func (e *Error) Error() string { return string(ErrorInvalidService) }

type commandRunner interface {
	Run(context.Context, string, []string) ([]byte, error)
}
type Service struct {
	runner   commandRunner
	bindings func(context.Context) ([]nativebinding.Check, error)
}
type commandSpec struct {
	executable string
	args       []string
	name       CheckName
}

var fixedCommands = []commandSpec{
	{ipPath, []string{"route"}, CheckDefaultRoute},
	{systemctlPath, []string{"show", "--property=ActiveState", "--value", "xray.service"}, CheckXrayService},
	{systemctlPath, []string{"show", "--property=ActiveState", "--value", "xray-tproxy-routing.service"}, CheckTProxyService},
}

func New() (*Service, error) { return NewWithXrayPath("/usr/local/etc/xray/config.json") }
func NewWithXrayPath(path string, networks ...bootstrap.NetworkConfig) (*Service, error) {
	var network bootstrap.NetworkConfig
	if len(networks) > 0 {
		network = networks[0]
	}
	runner, err := newBoundedRunner()
	if err != nil {
		return nil, &Error{Code: ErrorInvalidService}
	}
	return &Service{runner: runner, bindings: func(ctx context.Context) ([]nativebinding.Check, error) { return declaredChecks(ctx, path, network) }}, nil
}
func (service *Service) Diagnose(ctx context.Context) (Result, error) {
	if service == nil || service.runner == nil || service.bindings == nil || ctx == nil {
		return Result{}, &Error{Code: ErrorInvalidService}
	}
	result := Result{Checks: make([]Check, 0, 8)}
	for _, command := range fixedCommands {
		check := Check{Name: command.name, Status: StatusFailed}
		if err := ctx.Err(); err != nil {
			check.Status = contextStatus(err)
		} else {
			output, err := service.runner.Run(ctx, command.executable, append([]string(nil), command.args...))
			if err != nil {
				check.Status = commandStatus(err)
			} else {
				var condition bool
				if command.name == CheckDefaultRoute {
					condition, err = hasDefaultRoute(output)
				} else {
					condition, err = activeState(output)
				}
				if err == nil {
					check.Status = conditionStatus(condition)
				}
			}
		}
		result.Checks = append(result.Checks, check)
	}
	declared, err := service.bindings(ctx)
	if err != nil {
		result.Checks = append(result.Checks, Check{Name: CheckDeclaredBindings, Status: StatusUnavailable, Code: "declared_bindings_unavailable"})
	} else if len(declared) == 0 {
		result.Checks = append(result.Checks, Check{Name: CheckDeclaredBindings, Status: StatusNotDeclared})
	} else {
		for _, binding := range declared {
			status := CheckStatus(binding.Status)
			if binding.Status == "setup_only" {
				status = StatusNotDeclared
			}
			result.Checks = append(result.Checks, Check{Name: CheckDeclaredBindings, Status: status, TemplateID: binding.TemplateID, BindingID: binding.BindingID})
		}
	}
	result.Checks = append(result.Checks, Check{Name: CheckNativeEffects, Status: StatusUnchecked, Code: "native_effects_unchecked"})
	return result, nil
}
func conditionStatus(ok bool) CheckStatus {
	if ok {
		return StatusOK
	}
	return StatusMismatch
}
func contextStatus(err error) CheckStatus {
	if errors.Is(err, context.Canceled) {
		return StatusCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusTimedOut
	}
	return StatusFailed
}
func commandStatus(err error) CheckStatus {
	var failure *commandrunner.Error
	if errors.As(err, &failure) {
		switch failure.Failure {
		case commandrunner.FailureStart:
			return StatusUnavailable
		case commandrunner.FailureCanceled:
			return StatusCanceled
		case commandrunner.FailureTimeout:
			return StatusTimedOut
		default:
			return StatusFailed
		}
	}
	return contextStatus(err)
}

type boundedRunner struct{ ip, systemctl *commandrunner.Runner }

func newBoundedRunner() (*boundedRunner, error) {
	ip, err := commandrunner.New(ipPath, commandTimeout)
	if err != nil {
		return nil, err
	}
	service, err := commandrunner.New(systemctlPath, commandTimeout)
	if err != nil {
		return nil, err
	}
	return &boundedRunner{ip: ip, systemctl: service}, nil
}
func (r *boundedRunner) Run(ctx context.Context, exe string, args []string) ([]byte, error) {
	if r == nil || !fixedCommand(exe, args) {
		return nil, &commandrunner.Error{Failure: commandrunner.FailureInvalidInput}
	}
	runner := r.systemctl
	if exe == ipPath {
		runner = r.ip
	}
	result, err := runner.Run(ctx, args)
	return result.Stdout(), err
}
func fixedCommand(exe string, args []string) bool {
	for _, command := range fixedCommands {
		if exe == command.executable && equalArgs(args, command.args) {
			return true
		}
	}
	return false
}
func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
