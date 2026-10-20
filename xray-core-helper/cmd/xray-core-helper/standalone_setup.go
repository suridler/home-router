package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"home-router/xray-core-helper/internal/commandrunner"
	"home-router/xray-core-helper/internal/helperapp"
	"home-router/xray-core-helper/internal/selfupdate"
)

const (
	standaloneHelperUnit = "xray-core-helper.service"
	standaloneWebUnit    = "xrayui-web.service"
	runtimeLockPath      = "/var/lib/xray-core-helper-update/runtime.lock"
	systemctlPath        = "/usr/bin/systemctl"
	systemctlTimeout     = 2 * time.Minute
	restartTimeout       = 30 * time.Second
)

// standaloneSetupLifecycle isolates privileged lifecycle operations so tests
// can exercise ordering and failure handling without touching host services or
// the real runtime lock.
type standaloneSetupLifecycle interface {
	IsRoot() bool
	StopHelper(context.Context) error
	StopWebIfInstalled(context.Context) error
	AcquireRuntimeLock(context.Context) (func(), error)
	SetupClient(context.Context, string) (*http.Client, func(), error)
	RestartHelper(context.Context) error
}

func runStandaloneSetup(arguments []string, configPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	if !validSetupArguments(arguments, stderr) {
		return 2
	}
	if !standaloneStatusOnly(arguments) {
		if os.Geteuid() != 0 {
			fmt.Fprintln(stderr, "standalone setup requires root")
			return 1
		}
		release, err := selfupdate.Lock(selfupdate.StateRoot + "/operation.lock")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer release()
		op, err := selfupdate.NewStore(selfupdate.StateRoot).Load()
		if err != nil {
			fmt.Fprintln(stderr, "cannot read installation state")
			return 1
		}
		if op != nil && op.State != "succeeded" && op.State != "rolled_back" {
			fmt.Fprintln(stderr, "installation pending; finish installation or recovery before setup")
			return 1
		}
	}
	return runStandaloneSetupWithLifecycle(arguments, configPath, stdin, stdout, stderr, standaloneSystemLifecycle{})
}

// runStandaloneSetupWithLifecycle runs setup in-process while the daemon and web
// services are stopped. Status-only reads deliberately keep using the ordinary
// Unix-socket path.
func runStandaloneSetupWithLifecycle(arguments []string, configPath string, stdin io.Reader, stdout, stderr io.Writer, lifecycle standaloneSetupLifecycle) int {
	if !validSetupArguments(arguments, stderr) {
		return 2
	}
	if standaloneStatusOnly(arguments) {
		return runSetup(arguments, configPath, stdin, stdout, stderr)
	}
	if lifecycle == nil || !lifecycle.IsRoot() {
		fmt.Fprintln(stderr, "standalone setup requires root")
		return 1
	}
	ctx := context.Background()
	if err := lifecycle.StopHelper(ctx); err != nil {
		fmt.Fprintln(stderr, "could not stop xray-core-helper.service")
		return 1
	}
	restart := func(code int) int {
		restartCtx, cancel := context.WithTimeout(context.Background(), restartTimeout)
		defer cancel()
		if err := lifecycle.RestartHelper(restartCtx); err != nil {
			fmt.Fprintln(stderr, "could not restart xray-core-helper.service")
			if code == 0 {
				return 1
			}
		}
		return code
	}
	if err := lifecycle.StopWebIfInstalled(ctx); err != nil {
		fmt.Fprintln(stderr, "could not stop xrayui-web.service")
		return restart(1)
	}
	release, err := lifecycle.AcquireRuntimeLock(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "could not acquire runtime update lock")
		return restart(1)
	}
	client, closeClient, err := lifecycle.SetupClient(ctx, configPath)
	if err != nil {
		release()
		fmt.Fprintln(stderr, "could not initialize standalone setup")
		return restart(1)
	}
	if closeClient == nil {
		closeClient = func() {}
	}
	code := runSetupWithClient(client, arguments, stdin, stdout, stderr)
	closeClient()
	release()
	// The daemon must never start while this process holds the exclusive
	// runtime lock. A failed setup still restores the previous daemon state.
	return restart(code)
}

func standaloneStatusOnly(arguments []string) bool {
	if len(arguments) == 0 {
		return false
	}
	options := newSetupArguments(io.Discard)
	if options.flags.Parse(arguments[1:]) != nil || len(options.flags.Args()) != 0 {
		return false
	}
	return *options.statusOnly && !*options.recoverOnly
}

type standaloneSystemLifecycle struct{}

func (standaloneSystemLifecycle) IsRoot() bool {
	return os.Geteuid() == 0
}

func (standaloneSystemLifecycle) StopHelper(ctx context.Context) error {
	return runSystemctl(ctx, "stop", standaloneHelperUnit)
}

// StopWebIfInstalled matches the daemon's web runtime policy: a missing unit is
// ignored, while any other unit state is stopped before offline writes begin.
func (standaloneSystemLifecycle) StopWebIfInstalled(ctx context.Context) error {
	output, err := runSystemctlOutput(ctx, "show", "--property=LoadState", "--value", standaloneWebUnit)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(output)) == "not-found" {
		return nil
	}
	return runSystemctl(ctx, "stop", standaloneWebUnit)
}

func (standaloneSystemLifecycle) AcquireRuntimeLock(ctx context.Context) (func(), error) {
	return selfupdate.Lock(runtimeLockPath)
}

func (standaloneSystemLifecycle) SetupClient(ctx context.Context, configPath string) (*http.Client, func(), error) {
	return helperapp.SetupClient(ctx, configPath)
}

func (standaloneSystemLifecycle) RestartHelper(ctx context.Context) error {
	return runSystemctl(ctx, "restart", standaloneHelperUnit)
}

func runSystemctl(ctx context.Context, arguments ...string) error {
	_, err := runSystemctlOutput(ctx, arguments...)
	return err
}

func runSystemctlOutput(ctx context.Context, arguments ...string) ([]byte, error) {
	runner, err := commandrunner.New(systemctlPath, systemctlTimeout)
	if err != nil {
		return nil, err
	}
	result, err := runner.Run(ctx, arguments)
	if err != nil {
		return nil, err
	}
	return result.Stdout(), nil
}
