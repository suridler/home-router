package xrayvalidation

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/testconfig"
	"os"
	"reflect"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/commandrunner"
)

type runnerCall struct {
	executable string
	args       []string
}

type fakeRunner struct {
	err   error
	calls []runnerCall
}

func (runner *fakeRunner) Run(_ context.Context, executable string, args []string) error {
	runner.calls = append(runner.calls, runnerCall{
		executable: executable,
		args:       append([]string(nil), args...),
	})
	return runner.err
}

func TestValidateRunsExactConfiguredActiveConfigCommand(t *testing.T) {
	config := testconfig.Defaults()
	config.Xray.Config = t.TempDir() + "/active.jsonc"
	if err := os.WriteFile(config.Xray.Config, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	service, err := New(config, runner)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := service.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if result != (Result{Status: StatusValid}) {
		t.Fatalf("Validate() result = %#v; want valid projection", result)
	}
	want := []runnerCall{{
		executable: config.Xray.Binary,
		args:       []string{"run", "-test", "-config", config.Xray.Config},
	}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("commands = %#v; want %#v", runner.calls, want)
	}
}

func TestValidateMapsFailuresToRedactedTypedErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Code
	}{
		{
			name: "Xray rejects config",
			err:  &commandrunner.Error{Failure: commandrunner.FailureExit},
			want: CodeValidationFailed,
		},
		{
			name: "canceled",
			err:  &commandrunner.Error{Failure: commandrunner.FailureCanceled},
			want: CodeCanceled,
		},
		{
			name: "timeout",
			err:  &commandrunner.Error{Failure: commandrunner.FailureTimeout},
			want: CodeTimedOut,
		},
		{
			name: "runner failure includes private details",
			err:  errors.New("secret stderr at /private/config.json with private-uuid"),
			want: CodeCommandFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testconfig.Defaults()
			config.Xray.Config = t.TempDir() + "/active-secret.jsonc"
			if err := os.WriteFile(config.Xray.Config, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			runner := &fakeRunner{err: test.err}
			service, err := New(config, runner)
			if err != nil {
				t.Fatal(err)
			}

			result, err := service.Validate(context.Background())
			var validationErr *Error
			if !errors.As(err, &validationErr) || validationErr.Code != test.want {
				t.Fatalf("Validate() error = %v; want code %q", err, test.want)
			}
			if result != (Result{}) {
				t.Fatalf("failed validation returned projection %#v", result)
			}
			for _, secret := range []string{
				"secret stderr", "/private/config.json", "private-uuid",
				config.Xray.Binary, config.Xray.Config,
			} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestValidateUsesTypedContextFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want Code
	}{
		{
			name: "canceled before execution",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			want: CodeCanceled,
		},
		{
			name: "deadline before execution",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithTimeout(context.Background(), 0)
				return ctx, cancel
			},
			want: CodeTimedOut,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := testconfig.Defaults()
			config.Xray.Config = t.TempDir() + "/service-selected.json"
			runner := &fakeRunner{}
			service, err := New(config, runner)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := test.ctx()
			defer cancel()

			_, err = service.Validate(ctx)
			var validationErr *Error
			if !errors.As(err, &validationErr) || validationErr.Code != test.want {
				t.Fatalf("Validate() error = %v; want code %q", err, test.want)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("canceled validation invoked command: %#v", runner.calls)
			}
		})
	}
}

func TestNewRejectsInvalidConfiguredPaths(t *testing.T) {
	tests := []struct {
		name   string
		binary string
		config string
	}{
		{name: "relative binary", binary: "xray", config: "/etc/xray/config.json"},
		{name: "relative config", binary: "/usr/local/bin/xray", config: "config.json"},
		{name: "unclean config", binary: "/usr/local/bin/xray", config: "/etc/xray/../secret.json"},
		{name: "nul binary", binary: "/usr/local/bin/xray\x00--version", config: "/etc/xray/config.json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testconfig.Defaults()
			config.Xray.Binary = test.binary
			config.Xray.Config = test.config
			if _, err := New(config, &fakeRunner{}); err == nil {
				t.Fatal("New() succeeded with invalid configured paths")
			}
		})
	}
}

func TestValidateRejectsMalformedHelperMarkupBeforeXray(t *testing.T) {
	config := testconfig.Defaults()
	config.Xray.Config = t.TempDir() + "/config.json"
	if err := os.WriteFile(config.Xray.Config, []byte("{\n// helper:v1 invalid binding\n}"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	service, err := New(config, runner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Validate(context.Background())
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeMarkupInvalid || len(runner.calls) != 0 {
		t.Fatalf("failure=%v calls=%d", err, len(runner.calls))
	}
}
