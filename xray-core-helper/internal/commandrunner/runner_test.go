package commandrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunner(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		mode        string
		timeout     time.Duration
		cancel      bool
		wantFailure Failure
		wantOut     []byte
		wantErr     []byte
		wantTruncO  bool
		wantTruncE  bool
	}{
		{name: "success", mode: "success", timeout: 5 * time.Second, wantOut: []byte("safe stdout"), wantErr: []byte("safe stderr")},
		{name: "timeout", mode: "wait", timeout: 150 * time.Millisecond, wantFailure: FailureTimeout},
		{name: "caller cancellation", mode: "wait", timeout: 5 * time.Second, cancel: true, wantFailure: FailureCanceled},
		{name: "stdout limit", mode: "stdout", timeout: 5 * time.Second, wantFailure: FailureOutputLimit, wantOut: repeatedOutput("secret-out"), wantTruncO: true},
		{name: "stderr limit", mode: "stderr", timeout: 5 * time.Second, wantFailure: FailureOutputLimit, wantErr: repeatedOutput("secret-err"), wantTruncE: true},
		{name: "output-size limit", mode: "both", timeout: 5 * time.Second, wantFailure: FailureOutputLimit, wantOut: repeatedOutput("secret-out"), wantErr: repeatedOutput("secret-err"), wantTruncO: true, wantTruncE: true},
		{name: "redacted failure", mode: "fail", timeout: 5 * time.Second, wantFailure: FailureExit, wantOut: []byte("secret-output sensitive-argument"), wantErr: []byte("secret-error-output")},
	}

	t.Setenv("XCH_COMMAND_RUNNER_HELPER", "1")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("XCH_COMMAND_RUNNER_MODE", test.mode)
			runner, err := New(executable, test.timeout)
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()
			cancel := func() {}
			if test.cancel {
				var cancelContext context.CancelFunc
				ctx, cancelContext = context.WithCancel(ctx)
				cancel = cancelContext
				time.AfterFunc(250*time.Millisecond, cancel)
			}
			defer cancel()

			args := []string{"-test.run=^TestRunnerProcessHelper$", "--", "sensitive-argument"}
			result, runErr := runner.Run(ctx, args)
			if test.wantFailure == "" {
				if runErr != nil {
					t.Fatalf("Run() error = %v", runErr)
				}
			} else {
				var commandErr *Error
				if !errors.As(runErr, &commandErr) {
					t.Fatalf("Run() error = %v; want *Error", runErr)
				}
				if commandErr.Failure != test.wantFailure {
					t.Fatalf("failure = %q; want %q", commandErr.Failure, test.wantFailure)
				}
			}
			if !bytes.Equal(result.Stdout(), test.wantOut) || !bytes.Equal(result.Stderr(), test.wantErr) ||
				result.CapturedStdoutBytes != len(test.wantOut) || result.CapturedStderrBytes != len(test.wantErr) ||
				result.StdoutTruncated != test.wantTruncO || result.StderrTruncated != test.wantTruncE {
				t.Fatalf("result = %s; stdout/stderr do not match expected bounded bytes; truncation=%t/%t",
					result, result.StdoutTruncated, result.StderrTruncated)
			}
			if runErr != nil {
				assertRedacted(t, "error", []byte(fmt.Sprintf("%v %+v %#v", runErr, runErr, runErr)))
				assertRedacted(t, "formatted result", []byte(fmt.Sprintf("%v %+v %#v %s", result, result, result, result)))
				encoded, marshalErr := json.Marshal(result)
				if marshalErr != nil {
					t.Fatalf("marshal result: %v", marshalErr)
				}
				assertRedacted(t, "serialized result", encoded)
			}
			stdoutCopy := result.Stdout()
			if len(stdoutCopy) > 0 {
				stdoutCopy[0] ^= 0xff
				if bytes.Equal(result.Stdout(), stdoutCopy) {
					t.Fatal("mutating stdout accessor result changed stored capture")
				}
			}
		})
	}
}

func TestRunnerConfigurableOutputLimit(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XCH_COMMAND_RUNNER_HELPER", "1")
	t.Setenv("XCH_COMMAND_RUNNER_MODE", "success")

	runner, err := NewWithOutputLimit(executable, 5*time.Second, len("safe stdout")-1)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), []string{"-test.run=^TestRunnerProcessHelper$"})
	var commandErr *Error
	if !errors.As(err, &commandErr) || commandErr.Failure != FailureOutputLimit {
		t.Fatalf("Run() error = %v; want output-limit error", err)
	}
	if result.CapturedStdoutBytes != len("safe stdout")-1 || result.CapturedStderrBytes != len("safe stderr")-1 ||
		!result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("bounded result = %s; want both streams capped and marked truncated", result)
	}

	runner, err = NewWithOutputLimit(executable, 5*time.Second, len("safe stdout"))
	if err != nil {
		t.Fatal(err)
	}
	result, err = runner.Run(context.Background(), []string{"-test.run=^TestRunnerProcessHelper$"})
	if err != nil {
		t.Fatalf("Run() at exact output limit = %v", err)
	}
	if result.CapturedStdoutBytes != len("safe stdout") || result.StdoutTruncated ||
		result.CapturedStderrBytes != len("safe stderr") || result.StderrTruncated {
		t.Fatalf("exact-limit result = %s; want complete untruncated streams", result)
	}

	for _, limit := range []int{0, MaxOutputLimitBytes + 1} {
		if _, err := NewWithOutputLimit("/bin/true", time.Second, limit); err == nil {
			t.Errorf("NewWithOutputLimit(..., %d) succeeded; want invalid-input error", limit)
		} else if !errors.As(err, &commandErr) || commandErr.Failure != FailureInvalidInput {
			t.Errorf("NewWithOutputLimit(..., %d) error = %v; want invalid-input error", limit, err)
		}
	}
}

func assertRedacted(t *testing.T, label string, value []byte) {
	t.Helper()
	for _, secret := range []string{"sensitive-argument", "secret-output", "secret-error-output", "secret-out", "secret-err"} {
		if bytes.Contains(value, []byte(secret)) {
			t.Fatalf("%s exposed %q", label, secret)
		}
	}
}

func repeatedOutput(pattern string) []byte {
	output := bytes.Repeat([]byte(pattern), MaxOutputBytes/len(pattern)+1)
	return output[:MaxOutputBytes]
}

func TestRunnerRejectsInvalidRequestsWithoutLeakingExecutable(t *testing.T) {
	tests := []struct {
		name       string
		executable string
		timeout    time.Duration
		args       []string
	}{
		{name: "relative executable", executable: "sensitive-command-name", timeout: time.Second},
		{name: "nul executable", executable: "/bin/sensitive\x00command", timeout: time.Second},
		{name: "zero timeout", executable: "/bin/sensitive-command", timeout: 0},
		{name: "timeout above bound", executable: "/bin/sensitive-command", timeout: maxTimeout + time.Second},
		{name: "nul argument", executable: "/bin/true", timeout: time.Second, args: []string{"secret\x00argument"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New(test.executable, test.timeout)
			if err == nil {
				_, err = runner.Run(context.Background(), test.args)
			}
			var commandErr *Error
			if !errors.As(err, &commandErr) || commandErr.Failure != FailureInvalidInput {
				t.Fatalf("error = %v; want invalid-input command error", err)
			}
			for _, secret := range []string{"sensitive-command", "secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked input %q: %v", secret, err)
				}
			}
		})
	}
}

func TestRunnerClassifiesStartFailureWithoutLeakingExecutable(t *testing.T) {
	runner, err := New("/missing/sensitive-executable", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), nil)
	var commandErr *Error
	if !errors.As(err, &commandErr) || commandErr.Failure != FailureStart {
		t.Fatalf("error = %v; want start-failed command error", err)
	}
	if strings.Contains(err.Error(), "sensitive-executable") {
		t.Fatalf("error leaked executable: %v", err)
	}
}

func TestRunnerProcessHelper(t *testing.T) {
	if os.Getenv("XCH_COMMAND_RUNNER_HELPER") != "1" {
		return
	}
	mode := os.Getenv("XCH_COMMAND_RUNNER_MODE")
	switch mode {
	case "success":
		_, _ = os.Stdout.WriteString("safe stdout")
		_, _ = os.Stderr.WriteString("safe stderr")
	case "wait":
		time.Sleep(10 * time.Second)
	case "stdout":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("secret-out"), MaxOutputBytes/len("secret-out")+1))
	case "stderr":
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("secret-err"), MaxOutputBytes/len("secret-err")+1))
	case "both":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("secret-out"), MaxOutputBytes/len("secret-out")+1))
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("secret-err"), MaxOutputBytes/len("secret-err")+1))
	case "fail":
		_, _ = os.Stdout.WriteString("secret-output sensitive-argument")
		_, _ = os.Stderr.WriteString("secret-error-output")
		os.Exit(23)
	default:
		os.Exit(24)
	}
	os.Exit(0)
}
