// Package templatevalidation validates native configuration templates without
// exposing their contents or validator command output.
package templatevalidation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"home-router/xray-core-helper/internal/commandrunner"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/transaction"
)

const (
	maxNativeBytes = 1 << 20
	maxSysctlBytes = 64 << 10
	commandTimeout = 2 * time.Minute
)

const (
	xrayBinary    = "/usr/local/bin/xray"
	nftBinary     = "/usr/sbin/nft"
	systemdBinary = "/usr/bin/systemd-analyze"
	sysctlBinary  = "/usr/sbin/sysctl"
)

type Code string

const (
	CodeUnknownTemplate      Code = "unknown_template"
	CodeInvalidPath          Code = "invalid_path"
	CodeReadFailed           Code = "read_failed"
	CodeContentTooLarge      Code = "content_too_large"
	CodeInvalidSyntax        Code = "invalid_syntax"
	CodeCanceled             Code = "canceled"
	CodeTimedOut             Code = "timed_out"
	CodeValidatorUnavailable Code = "validator_unavailable"
	CodeValidatorUnsupported Code = "validator_unsupported"
)

// Error contains only a stable redacted code.
type Error struct{ Code Code }

func (failure *Error) Error() string {
	if failure == nil {
		return string(CodeValidatorUnavailable)
	}
	return string(failure.Code)
}

type Result struct {
	ID     templatecatalog.ID
	Status string
}

type CommandRunner interface {
	Run(context.Context, string, []string) error
}

type boundedRunners struct {
	runners map[string]*commandrunner.Runner
	xray    *transaction.XrayValidator
	binary  string
}

func newBoundedRunners(binary, assetDir string) (*boundedRunners, error) {
	runners := make(map[string]*commandrunner.Runner, 3)
	for _, executable := range []string{binary, nftBinary, systemdBinary, sysctlBinary} {
		runner, err := commandrunner.New(executable, commandTimeout)
		if err != nil {
			return nil, err
		}
		runners[executable] = runner
	}
	xray, err := transaction.NewXrayValidator(binary, assetDir)
	if err != nil {
		return nil, err
	}
	return &boundedRunners{runners: runners, xray: xray, binary: binary}, nil
}

func (runners *boundedRunners) Run(ctx context.Context, executable string, args []string) error {
	if executable == runners.binary {
		if len(args) != 4 || args[0] != "run" || args[1] != "-test" || args[2] != "-config" {
			return &commandrunner.Error{Failure: commandrunner.FailureInvalidInput}
		}
		bounded, cancel := context.WithTimeout(ctx, commandTimeout)
		defer cancel()
		if err := runners.xray.Validate(bounded, args[3]); err != nil {
			if bounded.Err() != nil {
				return bounded.Err()
			}
			return &commandrunner.Error{Failure: commandrunner.FailureExit}
		}
		return nil
	}
	runner, ok := runners.runners[executable]
	if !ok {
		return &commandrunner.Error{Failure: commandrunner.FailureInvalidInput}
	}
	_, err := runner.Run(ctx, args)
	return err
}

type Registry struct {
	binary  string
	catalog templatecatalog.Catalog
	runner  CommandRunner
}

func (runners *boundedRunners) Output(ctx context.Context, executable string, args []string) ([]byte, error) {
	runner, ok := runners.runners[executable]
	if !ok {
		return nil, &commandrunner.Error{Failure: commandrunner.FailureInvalidInput}
	}
	result, err := runner.Run(ctx, args)
	return result.Stdout(), err
}

func New(runner CommandRunner) (*Registry, error) {
	return NewWithAssets(runner, "/usr/local/share/xray")
}
func NewWithAssets(runner CommandRunner, assetDir string) (*Registry, error) {
	return NewWithXray(runner, xrayBinary, assetDir)
}
func NewWithXray(runner CommandRunner, binary, assetDir string) (*Registry, error) {
	if !cleanAbsolute(binary) || !cleanAbsolute(assetDir) {
		return nil, &Error{Code: CodeInvalidPath}
	}
	if runner == nil {
		bounded, err := newBoundedRunners(binary, assetDir)
		if err != nil {
			return nil, &Error{Code: CodeValidatorUnavailable}
		}
		runner = bounded
	}
	return &Registry{catalog: templatecatalog.Default(), runner: runner, binary: binary}, nil
}

// Validate checks one native candidate. The path is internal transaction data,
// never a transport-level free-form parameter.
func (registry *Registry) Validate(ctx context.Context, id templatecatalog.ID, path string) (Result, error) {
	if registry == nil || registry.runner == nil || ctx == nil {
		return Result{}, &Error{Code: CodeValidatorUnavailable}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, contextError(err)
	}
	if _, err := registry.catalog.Lookup(id); err != nil {
		return Result{}, &Error{Code: CodeUnknownTemplate}
	}
	if !cleanAbsolute(path) {
		return Result{}, &Error{Code: CodeInvalidPath}
	}

	var err error
	switch id {
	case templatecatalog.XrayConfig:
		err = registry.run(ctx, registry.binary, []string{"run", "-test", "-config", path})
	case templatecatalog.Nftables:
		_, err = readBounded(path, maxNativeBytes)
		if err == nil {
			err = registry.run(ctx, nftBinary, []string{"--check", "--file", path})
		}
	case templatecatalog.TProxyUnit:
		err = registry.run(ctx, systemdBinary, []string{"verify", path})
	case templatecatalog.Sysctl:
		_, err = readBounded(path, maxSysctlBytes)
		if err == nil {
			output, ok := registry.runner.(interface {
				Output(context.Context, string, []string) ([]byte, error)
			})
			if !ok {
				return Result{}, &Error{Code: CodeValidatorUnsupported}
			}
			help, helpErr := output.Output(ctx, sysctlBinary, []string{"--help"})
			if helpErr != nil {
				return Result{}, classify(helpErr)
			}
			if !strings.Contains(string(help), "--dry-run") {
				return Result{}, &Error{Code: CodeValidatorUnsupported}
			}
			err = registry.run(ctx, sysctlBinary, []string{"--dry-run", "--load", path})
		}
	default:
		return Result{}, &Error{Code: CodeUnknownTemplate}
	}
	if err != nil {
		return Result{}, classify(err)
	}
	return Result{ID: id, Status: "valid"}, nil
}

func (registry *Registry) run(ctx context.Context, executable string, args []string) error {
	if err := registry.runner.Run(ctx, executable, args); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	return ctx.Err()
}

var (
	errContentLarge = errors.New("native content exceeds limit")
	errReadFailed   = errors.New("native content unavailable")
)

func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errReadFailed
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errReadFailed
	}
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, errReadFailed
	}
	if int64(len(contents)) > limit {
		return nil, errContentLarge
	}
	return contents, nil
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, '\x00')
}

func classify(err error) *Error {
	if errors.Is(err, context.Canceled) {
		return &Error{Code: CodeCanceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimedOut}
	}
	if errors.Is(err, errReadFailed) {
		return &Error{Code: CodeReadFailed}
	}
	if errors.Is(err, errContentLarge) {
		return &Error{Code: CodeContentTooLarge}
	}
	var commandErr *commandrunner.Error
	if errors.As(err, &commandErr) {
		switch commandErr.Failure {
		case commandrunner.FailureCanceled:
			return &Error{Code: CodeCanceled}
		case commandrunner.FailureTimeout:
			return &Error{Code: CodeTimedOut}
		case commandrunner.FailureExit:
			return &Error{Code: CodeInvalidSyntax}
		}
	}
	return &Error{Code: CodeValidatorUnavailable}
}

func contextError(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimedOut}
	}
	return &Error{Code: CodeCanceled}
}

func (result Result) String() string {
	return fmt.Sprintf("template validation %s: %s", result.ID, result.Status)
}
