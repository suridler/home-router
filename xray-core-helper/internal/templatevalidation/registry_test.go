package templatevalidation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/commandrunner"
	"home-router/xray-core-helper/internal/templatecatalog"
)

type commandCall struct {
	executable string
	args       []string
}

func TestDiscoveredXrayExecutableIsUsedForNativeValidation(t *testing.T) {
	runner := &fakeRunner{}
	registry, err := NewWithXray(runner, "/opt/xray/xray", "/var/lib/xray-core-helper/geodata")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Validate(context.Background(), templatecatalog.XrayConfig, filepath.Join(t.TempDir(), "candidate.json")); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || runner.calls[0].executable != "/opt/xray/xray" {
		t.Fatal(runner.calls)
	}
}

type fakeRunner struct {
	err     error
	calls   []commandCall
	help    string
	helpErr error
}

func (runner *fakeRunner) Output(_ context.Context, executable string, args []string) ([]byte, error) {
	runner.calls = append(runner.calls, commandCall{executable: executable, args: append([]string(nil), args...)})
	if runner.helpErr != nil {
		return nil, runner.helpErr
	}
	if runner.help != "" {
		return []byte(runner.help), nil
	}
	return []byte("--dry-run"), nil
}

func (runner *fakeRunner) Run(_ context.Context, executable string, args []string) error {
	runner.calls = append(runner.calls, commandCall{executable: executable, args: append([]string(nil), args...)})
	return runner.err
}

func TestExternalValidatorsUseExactCommands(t *testing.T) {
	tests := []struct {
		id         templatecatalog.ID
		executable string
		args       func(string) []string
	}{
		{id: templatecatalog.XrayConfig, executable: xrayBinary, args: func(path string) []string { return []string{"run", "-test", "-config", path} }},
		{id: templatecatalog.Nftables, executable: nftBinary, args: func(path string) []string { return []string{"--check", "--file", path} }},
		{id: templatecatalog.TProxyUnit, executable: systemdBinary, args: func(path string) []string { return []string{"verify", path} }},
	}
	for _, test := range tests {
		t.Run(string(test.id), func(t *testing.T) {
			runner := &fakeRunner{}
			registry, err := New(runner)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "candidate")
			if test.id == templatecatalog.Nftables {
				if err := os.WriteFile(path, []byte("table inet xray {}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := registry.Validate(context.Background(), test.id, path)
			if err != nil {
				t.Fatal(err)
			}
			if result != (Result{ID: test.id, Status: "valid"}) {
				t.Fatalf("result = %#v", result)
			}
			want := []commandCall{{executable: test.executable, args: test.args(path)}}
			if !reflect.DeepEqual(runner.calls, want) {
				t.Fatalf("calls = %#v; want %#v", runner.calls, want)
			}
		})
	}
}

func TestLocalValidatorsAcceptRepositoryTemplates(t *testing.T) {
	registry, err := New(&fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..", "templConf")
	tests := []struct {
		id   templatecatalog.ID
		path string
	}{
		{id: templatecatalog.Sysctl, path: filepath.Join(root, "sysctl", "99-xray.conf")},
	}
	for _, test := range tests {
		t.Run(string(test.id), func(t *testing.T) {
			absolute, err := filepath.Abs(test.path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Validate(context.Background(), test.id, absolute); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestNativeValidatorBounds(t *testing.T) {
	tests := []struct {
		name    string
		id      templatecatalog.ID
		content string
		code    Code
	}{
		{name: "bounded content", id: templatecatalog.Sysctl, content: strings.Repeat("x", maxSysctlBytes+1), code: CodeContentTooLarge},
		{name: "bounded nft", id: templatecatalog.Nftables, content: strings.Repeat("x", maxNativeBytes+1), code: CodeContentTooLarge},
	}

	registry, err := New(&fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "candidate")
			if err := os.WriteFile(path, []byte(test.content), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := registry.Validate(context.Background(), test.id, path)
			assertCode(t, err, test.code)
		})
	}
}

func TestSysctlNativeDryRunWithoutGoSchema(t *testing.T) {
	for _, content := range []string{
		"kernel.hostname = example\n",
		"net.core.somaxconn = 4096\n",
		"net.ipv4.ip_forward = 1\nnet.ipv4.ip_forward = 0\n",
		"custom.key = a b c\n",
	} {
		path := filepath.Join(t.TempDir(), "candidate.conf")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		runner := &fakeRunner{}
		registry, _ := New(runner)
		if _, err := registry.Validate(context.Background(), templatecatalog.Sysctl, path); err != nil {
			t.Fatal(err)
		}
		want := []commandCall{
			{executable: sysctlBinary, args: []string{"--help"}},
			{executable: sysctlBinary, args: []string{"--dry-run", "--load", path}},
		}
		if !reflect.DeepEqual(runner.calls, want) {
			t.Fatal("unsafe command or missing capability check", runner.calls)
		}
	}
}

func TestSysctlUnsupportedNeverWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.conf")
	if err := os.WriteFile(path, []byte("custom.key = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{help: "old sysctl without capability"}
	registry, _ := New(runner)
	_, err := registry.Validate(context.Background(), templatecatalog.Sysctl, path)
	assertCode(t, err, CodeValidatorUnsupported)
	if len(runner.calls) != 1 || runner.calls[0].args[0] != "--help" {
		t.Fatal("unsupported validator attempted a write")
	}
}

func TestSysctlNativeFailureRedacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.conf")
	if err := os.WriteFile(path, []byte("invalid syntax\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, _ := New(&fakeRunner{err: &commandrunner.Error{Failure: commandrunner.FailureExit}})
	_, err := registry.Validate(context.Background(), templatecatalog.Sysctl, path)
	assertCode(t, err, CodeInvalidSyntax)
}

func TestRegistryReturnsStableRedactedFailures(t *testing.T) {
	tests := []struct {
		name   string
		id     templatecatalog.ID
		path   string
		runner error
		ctx    func() context.Context
		code   Code
	}{
		{name: "unknown template", id: "secret-template", path: "/tmp/candidate", code: CodeUnknownTemplate},
		{name: "relative path", id: templatecatalog.XrayConfig, path: "secret/config.json", code: CodeInvalidPath},
		{name: "path alias", id: templatecatalog.XrayConfig, path: "/tmp/secret/../config.json", code: CodeInvalidPath},
		{name: "missing local file", id: templatecatalog.Sysctl, path: "/tmp/missing-secret-config", code: CodeReadFailed},
		{name: "external reject", id: templatecatalog.XrayConfig, path: "/tmp/secret-config", runner: &commandrunner.Error{Failure: commandrunner.FailureExit}, code: CodeInvalidSyntax},
		{name: "external unavailable", id: templatecatalog.XrayConfig, path: "/tmp/secret-config", runner: errors.New("secret command output"), code: CodeValidatorUnavailable},
		{name: "external timeout", id: templatecatalog.TProxyUnit, path: "/tmp/secret-config", runner: &commandrunner.Error{Failure: commandrunner.FailureTimeout}, code: CodeTimedOut},
		{name: "canceled", id: templatecatalog.XrayConfig, path: "/tmp/secret-config", ctx: canceledContext, code: CodeCanceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, err := New(&fakeRunner{err: test.runner})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if test.ctx != nil {
				ctx = test.ctx()
			}
			_, err = registry.Validate(ctx, test.id, test.path)
			assertCode(t, err, test.code)
			for _, secret := range []string{"secret-template", "secret/config", "missing-secret", "secret-config", "secret command output"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func assertCode(t *testing.T, err error, want Code) {
	t.Helper()
	var validationErr *Error
	if !errors.As(err, &validationErr) || validationErr.Code != want {
		t.Fatalf("error = %v; want code %q", err, want)
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
