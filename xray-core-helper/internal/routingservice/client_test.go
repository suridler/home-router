package routingservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/xrayconfig"
)

const testPayload = `{"rules":[{"ruleTag":"managed","domain":["domain:example.test"]}],"balancers":[]}`

type runnerCall struct {
	executable string
	args       []string
}

type fakeRunner struct {
	calls    []runnerCall
	stdout   []byte
	stderr   []byte
	err      error
	failures int
	onCall   func(context.Context, string, []string)
}

func (runner *fakeRunner) Run(ctx context.Context, executable string, args []string) ([]byte, []byte, error) {
	runner.calls = append(runner.calls, runnerCall{executable: executable, args: append([]string(nil), args...)})
	if runner.onCall != nil {
		runner.onCall(ctx, executable, args)
	}
	if runner.failures > 0 {
		runner.failures--
		return runner.stdout, runner.stderr, runner.err
	}
	return runner.stdout, runner.stderr, nil
}

func newTestClient(t *testing.T, runner CommandRunner) *Client {
	t.Helper()
	client, err := NewClient("/usr/local/bin/xray", "127.0.0.1:10085", runner)
	if err != nil {
		t.Fatal(err)
	}
	client.wait = func(context.Context, time.Duration) error { return nil }
	return client
}

func TestApplyRoutingUsesFixedCommandAndSecureTemporaryPayload(t *testing.T) {
	runner := &fakeRunner{}
	client := newTestClient(t, runner)
	var payloadPath string
	runner.onCall = func(_ context.Context, executable string, args []string) {
		if executable != "/usr/local/bin/xray" {
			t.Fatalf("executable = %q", executable)
		}
		if len(args) != 6 || args[0] != "api" || args[1] != "adrules" ||
			args[2] != "--server=127.0.0.1:10085" || args[3] != "--timeout=15" || args[4] != "--append" {
			t.Fatalf("arguments = %#v", args)
		}
		payloadPath = args[5]
		info, err := os.Stat(payloadPath)
		if err != nil {
			t.Fatalf("stat payload: %v", err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("payload mode = %04o; want 0600", info.Mode().Perm())
		}
		content, err := os.ReadFile(payloadPath)
		if err != nil {
			t.Fatalf("read payload: %v", err)
		}
		var wrapped struct {
			Routing json.RawMessage `json:"routing"`
		}
		if err := json.Unmarshal(content, &wrapped); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if string(wrapped.Routing) != testPayload {
			t.Fatalf("routing payload = %s; want %s", wrapped.Routing, testPayload)
		}
	}

	if err := client.appendRouting(context.Background(), xrayconfig.RoutingPayload(testPayload)); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d; want 1", len(runner.calls))
	}
	if _, err := os.Stat(payloadPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary payload still exists: stat error = %v", err)
	}
}

func TestApplyRoutingRemovesTemporaryPayloadAfterCommandFailure(t *testing.T) {
	secret := "fixture-secret"
	runner := &fakeRunner{
		failures: maxAttempts,
		stdout:   []byte(secret),
		stderr:   []byte(secret),
		err:      errors.New(secret),
	}
	client := newTestClient(t, runner)
	var payloadPath string
	runner.onCall = func(_ context.Context, _ string, args []string) {
		payloadPath = args[5]
	}

	err := client.appendRouting(context.Background(), xrayconfig.RoutingPayload(testPayload))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error = %v; want redacted command failure", err)
	}
	if _, statErr := os.Stat(payloadPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temporary payload still exists: stat error = %v", statErr)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d; want one append attempt", len(runner.calls))
	}
}

func TestListRulesUsesFixedCommandAndReturnsOnlyStdout(t *testing.T) {
	runner := &fakeRunner{stdout: []byte(`{"rules":[]}`), stderr: []byte("ignored stderr")}
	client := newTestClient(t, runner)
	got, err := client.ListRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"rules":[]}` {
		t.Fatalf("stdout = %q", got)
	}
	want := runnerCall{
		executable: "/usr/local/bin/xray",
		args:       []string{"api", "lsrules", "--server=127.0.0.1:10085"},
	}
	if !reflect.DeepEqual(runner.calls, []runnerCall{want}) {
		t.Fatalf("calls = %#v; want %#v", runner.calls, []runnerCall{want})
	}
}

func TestListRulesRedactsCommandOutputFromErrors(t *testing.T) {
	secret := "fixture-secret"
	runner := &fakeRunner{
		failures: maxAttempts,
		stdout:   []byte(secret),
		stderr:   []byte(secret),
		err:      errors.New(secret),
	}
	client := newTestClient(t, runner)
	_, err := client.ListRules(context.Background())
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error = %v; want redacted command failure", err)
	}
	if len(runner.calls) != maxAttempts {
		t.Fatalf("calls = %d; want %d attempts", len(runner.calls), maxAttempts)
	}
}

func TestApplyRoutingCancellationSkipsCommandAndRemovesPayload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &fakeRunner{}
	client := newTestClient(t, runner)

	err := client.appendRouting(ctx, xrayconfig.RoutingPayload(testPayload))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context.Canceled", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("calls = %d; want none", len(runner.calls))
	}
}

func TestApplyRoutingPassesContextToCommandRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeRunner{
		failures: 1,
		err:      errors.New("fixture-secret"),
	}
	client := newTestClient(t, runner)
	runner.onCall = func(commandCtx context.Context, _ string, _ []string) {
		if commandCtx != ctx {
			t.Fatal("command runner received a different context")
		}
		cancel()
	}

	err := client.appendRouting(ctx, xrayconfig.RoutingPayload(testPayload))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context.Canceled", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d; want 1", len(runner.calls))
	}
}

func TestListRulesStopsRetriesWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeRunner{
		failures: 1,
		stdout:   []byte("fixture-secret"),
		stderr:   []byte("fixture-secret"),
		err:      errors.New("fixture-secret"),
	}
	client := newTestClient(t, runner)
	client.wait = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}

	_, err := client.ListRules(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context.Canceled", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d; want 1 before cancellation", len(runner.calls))
	}
}

func TestRoutingRetriesAreBoundedAndUseFixedDelay(t *testing.T) {
	runner := &fakeRunner{failures: 2, err: errors.New("temporary failure")}
	client := newTestClient(t, runner)
	var delays []time.Duration
	client.wait = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	if _, err := client.ListRules(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != maxAttempts {
		t.Fatalf("calls = %d; want %d", len(runner.calls), maxAttempts)
	}
	if !reflect.DeepEqual(delays, []time.Duration{retryDelay, retryDelay}) {
		t.Fatalf("delays = %#v; want two %s delays", delays, retryDelay)
	}
	for index, call := range runner.calls {
		if call.executable != "/usr/local/bin/xray" ||
			!reflect.DeepEqual(call.args[:3], []string{"api", "lsrules", "--server=127.0.0.1:10085"}) {
			t.Fatalf("attempt %d has unexpected command: %#v", index+1, call)
		}
	}
}

func TestNewClientRequiresConfiguredBinaryAndServer(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		binary string
		server string
	}{
		{name: "relative binary", binary: "xray", server: "127.0.0.1:10085"},
		{name: "empty server", binary: filepath.Join(t.TempDir(), "xray")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewClient(testCase.binary, testCase.server, &fakeRunner{}); err == nil {
				t.Fatal("NewClient() succeeded; want validation error")
			}
		})
	}
}
