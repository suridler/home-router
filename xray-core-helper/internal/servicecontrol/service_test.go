package servicecontrol

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/commandrunner"
)

type runnerFake struct {
	stdout []byte
	err    error
	calls  [][]string
}

func (runner *runnerFake) Run(_ context.Context, args []string) ([]byte, error) {
	runner.calls = append(runner.calls, append([]string(nil), args...))
	return append([]byte(nil), runner.stdout...), runner.err
}

func TestExecuteUsesExactAllowlistedCommands(t *testing.T) {
	tests := []struct {
		name       string
		unit       Unit
		action     Action
		wantArgs   []string
		wantStatus State
	}{
		{
			name: "Xray status", unit: UnitXray, action: ActionStatus,
			wantArgs:   []string{"show", "--property=ActiveState", "--value", "xray.service"},
			wantStatus: StateActive,
		},
		{
			name: "Xray start", unit: UnitXray, action: ActionStart,
			wantArgs: []string{"start", "xray.service"},
		},
		{
			name: "Xray stop", unit: UnitXray, action: ActionStop,
			wantArgs: []string{"stop", "xray.service"},
		},
		{
			name: "Xray restart", unit: UnitXray, action: ActionRestart,
			wantArgs: []string{"restart", "xray.service"},
		},
		{
			name: "TProxy status", unit: UnitTProxy, action: ActionStatus,
			wantArgs:   []string{"show", "--property=ActiveState", "--value", "xray-tproxy-routing.service"},
			wantStatus: StateActive,
		},
		{
			name: "TProxy start", unit: UnitTProxy, action: ActionStart,
			wantArgs: []string{"start", "xray-tproxy-routing.service"},
		},
		{
			name: "TProxy stop", unit: UnitTProxy, action: ActionStop,
			wantArgs: []string{"stop", "xray-tproxy-routing.service"},
		},
		{
			name: "TProxy restart", unit: UnitTProxy, action: ActionRestart,
			wantArgs: []string{"restart", "xray-tproxy-routing.service"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &runnerFake{stdout: []byte("active\n")}
			service := &Service{runner: runner}

			result, err := service.Execute(context.Background(), test.unit, test.action)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if !reflect.DeepEqual(runner.calls, [][]string{test.wantArgs}) {
				t.Fatalf("commands = %#v; want %#v", runner.calls, [][]string{test.wantArgs})
			}
			if result.Unit != test.unit || result.Action != test.action || result.State != test.wantStatus {
				t.Fatalf("result = %#v; want unit=%q action=%q state=%q",
					result, test.unit, test.action, test.wantStatus)
			}
		})
	}
}

func TestExecuteRejectsInvalidInputsWithoutRunningCommand(t *testing.T) {
	tests := []struct {
		name    string
		unit    Unit
		action  Action
		wantErr ErrorCode
	}{
		{name: "empty unit", unit: "", action: ActionStatus, wantErr: ErrorInvalidUnit},
		{name: "case-changed unit", unit: "Xray.service", action: ActionStatus, wantErr: ErrorInvalidUnit},
		{name: "bare unit near-match", unit: "xray", action: ActionStatus, wantErr: ErrorInvalidUnit},
		{name: "tproxy near-match", unit: "xray-tproxy.service", action: ActionStart, wantErr: ErrorInvalidUnit},
		{name: "unit suffix", unit: "xray.service.extra", action: ActionStop, wantErr: ErrorInvalidUnit},
		{name: "unit whitespace", unit: "xray.service ", action: ActionRestart, wantErr: ErrorInvalidUnit},
		{name: "unit injection", unit: "xray.service;touch /tmp/pwned", action: ActionStart, wantErr: ErrorInvalidUnit},
		{name: "unit option injection", unit: "--no-pager", action: ActionStatus, wantErr: ErrorInvalidUnit},
		{name: "unit NUL", unit: Unit("xray.service\x00--version"), action: ActionStatus, wantErr: ErrorInvalidUnit},
		{name: "empty action", unit: UnitXray, action: "", wantErr: ErrorInvalidAction},
		{name: "case-changed action", unit: UnitXray, action: "STATUS", wantErr: ErrorInvalidAction},
		{name: "unknown action", unit: UnitXray, action: "enable", wantErr: ErrorInvalidAction},
		{name: "action injection", unit: UnitXray, action: "start;id", wantErr: ErrorInvalidAction},
		{name: "action whitespace", unit: UnitTProxy, action: " restart", wantErr: ErrorInvalidAction},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &runnerFake{}
			service := &Service{runner: runner}

			_, err := service.Execute(context.Background(), test.unit, test.action)
			var controlError *Error
			if !errors.As(err, &controlError) || controlError.Code != test.wantErr {
				t.Fatalf("Execute() error = %v; want code %q", err, test.wantErr)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("rejected request invoked commands: %#v", runner.calls)
			}
		})
	}
}

func TestStatusMapsKnownSystemdStatesToTypedResults(t *testing.T) {
	states := []State{
		StateActive, StateReloading, StateInactive, StateFailed, StateActivating,
		StateDeactivating, StateMaintenance, StateRefreshing,
	}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			runner := &runnerFake{stdout: []byte(string(state) + "\n")}
			service := &Service{runner: runner}

			result, err := service.Execute(context.Background(), UnitXray, ActionStatus)
			if err != nil || result.State != state {
				t.Fatalf("Execute() = %#v, %v; want state %q", result, err, state)
			}
		})
	}
}

func TestExecuteNeverReturnsCommandOutput(t *testing.T) {
	const secret = "systemd-secret-output"
	for _, test := range []struct {
		name   string
		action Action
		stdout []byte
		err    error
	}{
		{name: "action output ignored", action: ActionRestart, stdout: []byte(secret)},
		{name: "command error redacted", action: ActionStart, stdout: []byte(secret), err: errors.New(secret)},
		{name: "unknown status redacted", action: ActionStatus, stdout: []byte(secret)},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &runnerFake{stdout: test.stdout, err: test.err}
			service := &Service{runner: runner}

			result, err := service.Execute(context.Background(), UnitXray, test.action)
			formatted := strings.Join([]string{
				string(result.Unit), string(result.Action), string(result.State),
			}, " ")
			if strings.Contains(formatted, secret) {
				t.Fatalf("result exposed command output: %q", formatted)
			}
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Fatalf("error exposed command output: %v", err)
			}
			if test.action == ActionStatus && err == nil {
				t.Fatal("unknown status output unexpectedly succeeded")
			}
		})
	}
}

func TestXrayRestarterUsesOnlyAllowlistedXrayRestart(t *testing.T) {
	runner := &runnerFake{stdout: []byte("secret command output")}
	services := &Service{runner: runner}
	restarter, err := NewXrayRestarter(services)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarter.Restart(context.Background()); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if !reflect.DeepEqual(runner.calls, [][]string{{"restart", "xray.service"}}) {
		t.Fatalf("restart commands = %#v; want Xray-only restart", runner.calls)
	}
}

func TestExecuteReturnsTypedBoundedCommandFailures(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr ErrorCode
	}{
		{name: "canceled", err: &commandrunner.Error{Failure: commandrunner.FailureCanceled}, wantErr: ErrorCanceled},
		{name: "timeout", err: &commandrunner.Error{Failure: commandrunner.FailureTimeout}, wantErr: ErrorTimedOut},
		{name: "exit", err: &commandrunner.Error{Failure: commandrunner.FailureExit}, wantErr: ErrorCommandFailed},
		{name: "unknown", err: errors.New("private systemctl output"), wantErr: ErrorCommandFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &Service{runner: &runnerFake{err: test.err}}
			_, err := service.Execute(context.Background(), UnitTProxy, ActionRestart)

			var controlError *Error
			if !errors.As(err, &controlError) || controlError.Code != test.wantErr {
				t.Fatalf("Execute() error = %v; want code %q", err, test.wantErr)
			}
		})
	}
}

func TestRoutingActionsDoNotRequireGatewayDiagnostics(t *testing.T) {
	for _, action := range []Action{ActionStart, ActionRestart} {
		t.Run(string(action), func(t *testing.T) {
			runner := &runnerFake{}
			service := &Service{runner: runner}
			if _, err := service.Execute(context.Background(), UnitTProxy, action); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(runner.calls, [][]string{{string(action), string(UnitTProxy)}}) {
				t.Fatal(runner.calls)
			}
		})
	}
	service := &Service{runner: &runnerFake{}}
	if _, err := service.Execute(context.Background(), UnitTProxy, Action("enable")); err == nil {
		t.Fatal("enabled arbitrary unit")
	}
}
