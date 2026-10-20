package logrotation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
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

type pathsFake struct {
	present map[string]bool
	calls   []string
}

func (paths *pathsFake) ExistsRegular(path string) bool {
	paths.calls = append(paths.calls, path)
	return paths.present[path]
}

func installedPaths() *pathsFake {
	present := make(map[string]bool, len(installationPaths))
	for _, path := range installationPaths {
		present[path] = true
	}
	return &pathsFake{present: present}
}

func request(access, errorMode api.LogTargetMode, rotation *bool) api.LogConfigurationRequest {
	result := api.LogConfigurationRequest{
		Access: api.LogTarget{Mode: access},
		Error:  api.LogTarget{Mode: errorMode},
	}
	if rotation != nil {
		result.Rotation = &api.LogRotationRequest{Enabled: *rotation}
	}
	return result
}

func boolPointer(value bool) *bool {
	return &value
}

func TestApplyUsesOnlyFixedXrayInstallTimerCommands(t *testing.T) {
	tests := []struct {
		name      string
		rotation  bool
		wantState State
		wantArgs  []string
	}{
		{name: "enable", rotation: true, wantState: StateEnabled, wantArgs: []string{"enable", "--now", timerUnit}},
		{name: "disable", rotation: false, wantState: StateDisabled, wantArgs: []string{"disable", "--now", timerUnit}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &runnerFake{}
			paths := installedPaths()
			service := &Service{runner: runner, paths: paths}

			result, err := service.Apply(context.Background(), request(api.LogTargetFile, api.LogTargetJournal, &test.rotation))
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if result != (Result{State: test.wantState}) {
				t.Fatalf("result = %#v; want state %q", result, test.wantState)
			}
			if !reflect.DeepEqual(runner.calls, [][]string{test.wantArgs}) {
				t.Fatalf("commands = %#v; want %#v", runner.calls, [][]string{test.wantArgs})
			}
			if !reflect.DeepEqual(paths.calls, installationPaths) {
				t.Fatalf("checked paths = %#v; want %#v", paths.calls, installationPaths)
			}
		})
	}
}

func TestApplyLeavesTimerUnchangedWhenRotationIsOmitted(t *testing.T) {
	runner := &runnerFake{}
	paths := &pathsFake{present: map[string]bool{}}
	service := &Service{runner: runner, paths: paths}

	result, err := service.Apply(context.Background(), request(api.LogTargetJournal, api.LogTargetFile, nil))
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result != (Result{State: StateUnchanged}) {
		t.Fatalf("result = %#v; want unchanged", result)
	}
	if len(runner.calls) != 0 || len(paths.calls) != 0 {
		t.Fatalf("unchanged request had side effects: commands=%#v paths=%#v", runner.calls, paths.calls)
	}
}

func TestApplyForcesDisableWhenNoFileBackedLogsRemain(t *testing.T) {
	for _, test := range []struct {
		name     string
		access   api.LogTargetMode
		errorLog api.LogTargetMode
	}{
		{name: "both disabled", access: api.LogTargetDisabled, errorLog: api.LogTargetDisabled},
		{name: "journal and disabled", access: api.LogTargetJournal, errorLog: api.LogTargetDisabled},
		{name: "both journal", access: api.LogTargetJournal, errorLog: api.LogTargetJournal},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &runnerFake{}
			service := &Service{runner: runner, paths: installedPaths()}

			result, err := service.Apply(context.Background(), request(test.access, test.errorLog, boolPointer(true)))
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if result != (Result{State: StateDisabled, Forced: true}) {
				t.Fatalf("result = %#v; want forced disabled", result)
			}
			want := [][]string{{"disable", "--now", timerUnit}}
			if !reflect.DeepEqual(runner.calls, want) {
				t.Fatalf("commands = %#v; want %#v", runner.calls, want)
			}
		})
	}
}

func TestApplyHandlesMissingInstallation(t *testing.T) {
	for _, missing := range installationPaths {
		t.Run(filepath.Base(missing), func(t *testing.T) {
			paths := installedPaths()
			paths.present[missing] = false
			runner := &runnerFake{}
			service := &Service{runner: runner, paths: paths}

			_, err := service.Apply(context.Background(), request(api.LogTargetFile, api.LogTargetDisabled, boolPointer(true)))
			var rotationError *Error
			if !errors.As(err, &rotationError) || rotationError.Code != ErrorMissingInstallation {
				t.Fatalf("Apply() error = %v; want %q", err, ErrorMissingInstallation)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("missing installation invoked command: %#v", runner.calls)
			}
		})
	}
}

func TestForcedDisableToleratesMissingInstallation(t *testing.T) {
	runner := &runnerFake{}
	paths := &pathsFake{present: map[string]bool{}}
	service := &Service{runner: runner, paths: paths}

	result, err := service.Apply(context.Background(), request(api.LogTargetJournal, api.LogTargetDisabled, nil))
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result != (Result{State: StateDisabled, Forced: true}) {
		t.Fatalf("result = %#v; want forced disabled", result)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("absent timer invoked command: %#v", runner.calls)
	}
}

func TestForcedDisableStillRunsForPartialInstallationWithTimer(t *testing.T) {
	paths := &pathsFake{present: map[string]bool{timerTemplatePath: true}}
	runner := &runnerFake{}
	service := &Service{runner: runner, paths: paths}

	result, err := service.Apply(context.Background(), request(api.LogTargetDisabled, api.LogTargetJournal, nil))
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result != (Result{State: StateDisabled, Forced: true}) {
		t.Fatalf("result = %#v; want forced disabled", result)
	}
	want := [][]string{{"disable", "--now", timerUnit}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("commands = %#v; want %#v", runner.calls, want)
	}
}

func TestApplyMapsCommandFailuresWithoutLeakingDetails(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code ErrorCode
	}{
		{name: "canceled", err: &commandrunner.Error{Failure: commandrunner.FailureCanceled}, code: ErrorCanceled},
		{name: "timeout", err: &commandrunner.Error{Failure: commandrunner.FailureTimeout}, code: ErrorTimedOut},
		{name: "exit", err: &commandrunner.Error{Failure: commandrunner.FailureExit}, code: ErrorCommandFailed},
		{name: "unknown", err: errors.New("private systemctl output"), code: ErrorCommandFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &Service{runner: &runnerFake{err: test.err}, paths: installedPaths()}
			_, err := service.Apply(context.Background(), request(api.LogTargetFile, api.LogTargetFile, boolPointer(true)))
			var rotationError *Error
			if !errors.As(err, &rotationError) || rotationError.Code != test.code {
				t.Fatalf("Apply() error = %v; want %q", err, test.code)
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "systemctl output") {
				t.Fatalf("error leaked command details: %v", err)
			}
		})
	}
}

func TestOSPathCheckerRejectsMissingDirectoriesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "policy")
	if err := os.WriteFile(regular, []byte("rotate"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "policy-link")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	checker := osPathChecker{}
	if !checker.ExistsRegular(regular) {
		t.Fatal("regular policy was not recognized")
	}
	if checker.ExistsRegular(root) || checker.ExistsRegular(symlink) || checker.ExistsRegular(filepath.Join(root, "missing")) {
		t.Fatal("checker accepted a directory, symlink, or missing path")
	}
}

func TestPrepareSnapshotsAndRestoresTimerState(t *testing.T) {
	tests := []struct {
		name      string
		previous  string
		desired   bool
		wantCalls [][]string
		rollback  bool
	}{
		{
			name: "enable then rollback", previous: "disabled\n", desired: true, rollback: true,
			wantCalls: [][]string{
				{"show", "--property=UnitFileState", "--value", timerUnit},
				{"enable", "--now", timerUnit},
				{"disable", "--now", timerUnit},
			},
		},
		{
			name: "already disabled", previous: "disabled\n", desired: false,
			wantCalls: [][]string{{"show", "--property=UnitFileState", "--value", timerUnit}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &runnerFake{stdout: []byte(test.previous)}
			service := &Service{runner: runner, paths: installedPaths()}
			snapshot, err := service.Prepare(context.Background(), request(api.LogTargetFile, api.LogTargetJournal, &test.desired))
			if err != nil {
				t.Fatalf("Prepare() error = %v", err)
			}
			if test.rollback {
				if err := snapshot.Rollback(); err != nil {
					t.Fatalf("Rollback() error = %v", err)
				}
			} else if err := snapshot.Commit(); err != nil {
				t.Fatalf("Commit() error = %v", err)
			}
			if !reflect.DeepEqual(runner.calls, test.wantCalls) {
				t.Fatalf("commands = %#v; want %#v", runner.calls, test.wantCalls)
			}
		})
	}
}

func TestPrepareNoOpDoesNotInspectTimer(t *testing.T) {
	runner := &runnerFake{}
	service := &Service{runner: runner, paths: &pathsFake{present: map[string]bool{}}}
	snapshot, err := service.Prepare(context.Background(), request(api.LogTargetFile, api.LogTargetJournal, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Rollback(); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unchanged preparation invoked commands: %#v", runner.calls)
	}
}
