// Package commandrunner provides a bounded, context-aware boundary for fixed
// external executables.
package commandrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	MaxOutputBytes      = 64 << 10
	MaxOutputLimitBytes = 1 << 20
	maxTimeout          = 2 * time.Minute
	waitDelay           = time.Second
)

type Failure string

const (
	FailureInvalidInput Failure = "invalid_input"
	FailureCanceled     Failure = "canceled"
	FailureTimeout      Failure = "timeout"
	FailureStart        Failure = "start_failed"
	FailureExit         Failure = "exit_failed"
	FailureOutputLimit  Failure = "output_limit"
)

// Error classifies a command failure without retaining arguments or output.
type Error struct {
	Failure  Failure
	ExitCode int
}

func (err *Error) Error() string {
	switch err.Failure {
	case FailureInvalidInput:
		return "external command request is invalid"
	case FailureCanceled:
		return "external command was canceled"
	case FailureTimeout:
		return "external command timed out"
	case FailureStart:
		return "external command could not start"
	case FailureExit:
		return "external command failed"
	case FailureOutputLimit:
		return "external command output exceeded the limit"
	default:
		return "external command failed"
	}
}

// Result exposes bounded output only through explicit accessors. Its fields
// and formatting contain metadata only.
type Result struct {
	CapturedStdoutBytes int
	CapturedStderrBytes int
	StdoutTruncated     bool
	StderrTruncated     bool
	stdout              []byte
	stderr              []byte
}

// Stdout returns a copy of the bounded captured stdout.
func (result Result) Stdout() []byte {
	return append([]byte(nil), result.stdout...)
}

// Stderr returns a copy of the bounded captured stderr.
func (result Result) Stderr() []byte {
	return append([]byte(nil), result.stderr...)
}

func (result Result) String() string {
	return fmt.Sprintf("command result (stdout: %d bytes, truncated: %t; stderr: %d bytes, truncated: %t)",
		result.CapturedStdoutBytes, result.StdoutTruncated, result.CapturedStderrBytes, result.StderrTruncated)
}

func (result Result) GoString() string {
	return result.String()
}

func (result Result) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, result.String())
}

// Runner binds one absolute executable to a fixed execution timeout and
// per-stream output bound.
type Runner struct {
	executable  string
	timeout     time.Duration
	outputLimit int
}

func New(executable string, timeout time.Duration) (*Runner, error) {
	return NewWithOutputLimit(executable, timeout, MaxOutputBytes)
}

// NewWithOutputLimit binds a fixed executable to an explicit per-stream output
// bound. The configured bound cannot exceed MaxOutputLimitBytes.
func NewWithOutputLimit(executable string, timeout time.Duration, outputLimit int) (*Runner, error) {
	if !filepath.IsAbs(executable) || strings.ContainsRune(executable, '\x00') ||
		timeout <= 0 || timeout > maxTimeout ||
		outputLimit < 1 || outputLimit > MaxOutputLimitBytes {
		return nil, &Error{Failure: FailureInvalidInput}
	}
	return &Runner{executable: executable, timeout: timeout, outputLimit: outputLimit}, nil
}

func (runner *Runner) Run(ctx context.Context, args []string) (Result, error) {
	if runner == nil || ctx == nil {
		return Result{}, &Error{Failure: FailureInvalidInput}
	}
	for _, arg := range args {
		if strings.ContainsRune(arg, '\x00') {
			return Result{}, &Error{Failure: FailureInvalidInput}
		}
	}
	if ctx.Err() != nil {
		return Result{}, &Error{Failure: FailureCanceled}
	}

	commandCtx, cancel := context.WithTimeout(ctx, runner.timeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, runner.executable, args...)
	command.WaitDelay = waitDelay
	stdout := newCapture(runner.outputLimit)
	stderr := newCapture(runner.outputLimit)
	command.Stdout = stdout
	command.Stderr = stderr

	runErr := command.Run()
	result := Result{
		CapturedStdoutBytes: len(stdout.content),
		CapturedStderrBytes: len(stderr.content),
		StdoutTruncated:     stdout.truncated,
		StderrTruncated:     stderr.truncated,
		stdout:              stdout.content,
		stderr:              stderr.content,
	}
	if ctx.Err() != nil {
		return result, &Error{Failure: FailureCanceled}
	}
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		return result, &Error{Failure: FailureTimeout}
	}
	if stdout.truncated || stderr.truncated {
		return result, &Error{Failure: FailureOutputLimit}
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return result, &Error{Failure: FailureExit, ExitCode: exitErr.ExitCode()}
		}
		return result, &Error{Failure: FailureStart}
	}
	return result, nil
}

type capture struct {
	content   []byte
	truncated bool
	limit     int
}

func newCapture(limit int) *capture {
	return &capture{content: make([]byte, 0, limit), limit: limit}
}

func (capture *capture) Write(data []byte) (int, error) {
	remaining := capture.limit - len(capture.content)
	if remaining > 0 {
		captured := len(data)
		if captured > remaining {
			captured = remaining
		}
		capture.content = append(capture.content, data[:captured]...)
	}
	if len(data) > remaining {
		capture.truncated = true
	}
	return len(data), nil
}
