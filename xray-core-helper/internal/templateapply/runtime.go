package templateapply

import (
	"context"
	"errors"
	"os"
	"time"

	"home-router/xray-core-helper/internal/commandrunner"
	"home-router/xray-core-helper/internal/nativeblock"
	"home-router/xray-core-helper/internal/routingservice"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
)

const (
	systemctlBinary = "/usr/bin/systemctl"
	nftBinary       = "/usr/sbin/nft"
	runtimeTimeout  = 2 * time.Minute
)

type Runner interface {
	Run(context.Context, string, []string) error
}

type boundedRuntimeRunner struct {
	runners map[string]*commandrunner.Runner
}

func newBoundedRuntimeRunner() (*boundedRuntimeRunner, error) {
	result := &boundedRuntimeRunner{runners: make(map[string]*commandrunner.Runner, 2)}
	for _, executable := range []string{systemctlBinary, nftBinary} {
		runner, err := commandrunner.New(executable, runtimeTimeout)
		if err != nil {
			return nil, err
		}
		result.runners[executable] = runner
	}
	return result, nil
}
func (runner *boundedRuntimeRunner) Output(ctx context.Context, executable string, args []string) ([]byte, error) {
	bound, ok := runner.runners[executable]
	if !ok {
		return nil, errors.New("executable not allowed")
	}
	result, err := bound.Run(ctx, args)
	return result.Stdout(), err
}
func (runner *boundedRuntimeRunner) Run(ctx context.Context, executable string, args []string) error {
	bound, ok := runner.runners[executable]
	if !ok {
		return errors.New("runtime executable is not allowlisted")
	}
	_, err := bound.Run(ctx, args)
	return err
}

type CommandRuntime struct {
	systemNftPath string // test-only path substitution
	runner        Runner
	verifyAPI     func(context.Context, string) error
}

func NewCommandRuntime() (*CommandRuntime, error) {
	return NewCommandRuntimeWithXray("/usr/local/bin/xray")
}
func NewCommandRuntimeWithXray(binary string, configPaths ...string) (*CommandRuntime, error) {
	runner, err := newBoundedRuntimeRunner()
	if err != nil {
		return nil, err
	}
	return &CommandRuntime{runner: runner, verifyAPI: func(ctx context.Context, endpoint string) error { return verifyAPIWithBinary(ctx, binary, endpoint) }}, nil
}

func newCommandRuntime(runner Runner) (*CommandRuntime, error) {
	if runner == nil {
		return nil, &Error{Code: CodeRuntime}
	}
	return &CommandRuntime{runner: runner}, nil
}

func (runtime *CommandRuntime) Prepare(ctx context.Context, id templatecatalog.ID) (RuntimeTransaction, error) {
	if runtime == nil || runtime.runner == nil {
		return nil, &Error{Code: CodeRuntime}
	}
	transaction := &commandRuntimeTransaction{id: id, runner: runtime.runner, verifyAPI: runtime.verifyAPI, systemNftPath: runtime.systemNftPath}
	switch id {
	case templatecatalog.XrayConfig, templatecatalog.TProxyUnit:
		unit := unitFor(id)
		if err := runtime.runner.Run(ctx, systemctlBinary, []string{"is-active", "--quiet", unit}); err == nil {
			transaction.wasActive = true
		}
	case templatecatalog.Nftables, templatecatalog.Sysctl:
	default:
		return nil, &Error{Code: CodeUnknownTemplate}
	}
	return transaction, nil
}

type commandRuntimeTransaction struct {
	systemNftPath string
	id            templatecatalog.ID
	runner        Runner
	wasActive     bool
	path          string
	verifyAPI     func(context.Context, string) error
}

// BeforeReplace stops the old unit while systemd still has its old mark/table
// and cleanup executable. Called only after durable safe-apply evidence exists.
func (transaction *commandRuntimeTransaction) BeforeReplace(ctx context.Context) error {
	if transaction.id == templatecatalog.TProxyUnit {
		return transaction.run(ctx, systemctlBinary, "stop", "xray-tproxy-routing.service")
	}
	return nil
}

func (transaction *commandRuntimeTransaction) Apply(ctx context.Context, path string) error {
	transaction.path = path

	switch transaction.id {
	case templatecatalog.XrayConfig:
		return transaction.run(ctx, systemctlBinary, "restart", "xray.service")
	case templatecatalog.Nftables:
		return transaction.applyNft(ctx, path)
	case templatecatalog.TProxyUnit:
		if err := transaction.run(ctx, systemctlBinary, "daemon-reload"); err != nil {
			return err
		}
		if err := transaction.run(ctx, systemctlBinary, "start", "xray-core-helper-sysctl.service"); err != nil {
			return err
		}
		nftPath := transaction.systemNftPath
		if nftPath == "" {
			nftPath = "/etc/nftables.conf"
		}
		if err := transaction.applyNft(ctx, nftPath); err != nil {
			return err
		}
		return transaction.run(ctx, systemctlBinary, "restart", "xray.service")
	case templatecatalog.Sysctl:
		return transaction.run(ctx, systemctlBinary, "start", "xray-core-helper-sysctl.service")
	default:
		return &Error{Code: CodeUnknownTemplate}
	}
}

func (transaction *commandRuntimeTransaction) Healthy(ctx context.Context) error {
	switch transaction.id {
	case templatecatalog.XrayConfig:
		if err := transaction.run(ctx, systemctlBinary, "is-active", "--quiet", "xray.service"); err != nil {
			return err
		}
		raw, err := readEffect(transaction.path)
		if err != nil || transaction.verifyAPI == nil {
			return &Error{Code: CodeXrayEffect}
		}
		endpoint, err := xrayconfig.ExtractLocalAPI(raw)
		if err != nil {
			return &Error{Code: CodeXrayEffect}
		}
		if err = transaction.verifyAPI(ctx, endpoint); err != nil {
			return &Error{Code: CodeXrayEffect}
		}
		return nil
	case templatecatalog.Nftables:
		// Successful native loading does not prove arbitrary rule effects.
		return nil
	case templatecatalog.TProxyUnit:
		if err := transaction.run(ctx, systemctlBinary, "is-active", "--quiet", "xray-tproxy-routing.service"); err != nil {
			return err
		}
		return nil // arbitrary native command effects and LAN forwarding remain unchecked
	case templatecatalog.Sysctl:
		return nil // native success is not proof of arbitrary sysctl effects
	default:
		return &Error{Code: CodeUnknownTemplate}
	}
}

func verifyLocalAPI(ctx context.Context, endpoint string) error {
	return verifyAPIWithBinary(ctx, "/usr/local/bin/xray", endpoint)
}
func verifyAPIWithBinary(ctx context.Context, binary, endpoint string) error {
	client, err := routingservice.NewClient(binary, endpoint, nil)
	if err != nil {
		return err
	}
	_, err = client.ListRules(ctx)
	return err
}
func readEffect(path string) ([]byte, error) {
	raw, _, _, _, _, err := readRegular(path)
	return raw, err
}

func (transaction *commandRuntimeTransaction) Rollback(ctx context.Context, path string) error {
	switch transaction.id {
	case templatecatalog.XrayConfig:
		return transaction.restoreService(ctx, "xray.service", false)
	case templatecatalog.Nftables, templatecatalog.Sysctl, templatecatalog.TProxyUnit:
		return &Error{Code: CodeSafeApply} // Engine restores file; operator reboots
	default:
		return &Error{Code: CodeUnknownTemplate}
	}
}

func (transaction *commandRuntimeTransaction) restoreService(ctx context.Context, unit string, restartInactive bool) error {
	if transaction.wasActive || restartInactive {
		if transaction.wasActive {
			return transaction.run(ctx, systemctlBinary, "restart", unit)
		}
		return transaction.run(ctx, systemctlBinary, "stop", unit)
	}
	return transaction.run(ctx, systemctlBinary, "stop", unit)
}
func (transaction *commandRuntimeTransaction) run(ctx context.Context, executable string, args ...string) error {
	return transaction.runner.Run(ctx, executable, args)
}

func (transaction *commandRuntimeTransaction) applyNft(ctx context.Context, path string) error {
	raw, err := readEffect(path)
	if err != nil {
		return err
	}
	block, err := nativeblock.Build(nil, raw, true)
	if err != nil {
		return err
	}
	batch, err := os.CreateTemp("", "helper-native-batch-*.nft")
	if err != nil {
		return err
	}
	defer os.Remove(batch.Name())
	if _, err = batch.Write(block.Content); err != nil {
		batch.Close()
		return err
	}
	if err = batch.Close(); err != nil {
		return err
	}
	if err = transaction.run(ctx, nftBinary, "--check", "--file", batch.Name()); err != nil {
		return err
	}
	if err = transaction.run(ctx, nftBinary, "--file", batch.Name()); err != nil {
		return err
	}

	return nil
}

func unitFor(id templatecatalog.ID) string {
	switch id {
	case templatecatalog.XrayConfig:
		return "xray.service"
	case templatecatalog.TProxyUnit:
		return "xray-tproxy-routing.service"
	default:
		return ""
	}
}
