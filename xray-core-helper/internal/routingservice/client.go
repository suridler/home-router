package routingservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"home-router/xray-core-helper/internal/commandrunner"
	"home-router/xray-core-helper/internal/xrayconfig"
)

const (
	maxAttempts    = 3
	retryDelay     = 100 * time.Millisecond
	commandTimeout = 15 * time.Second
)

type CommandRunner interface {
	Run(ctx context.Context, executable string, args []string) (stdout, stderr []byte, err error)
}

type Client struct {
	binary string
	server string
	runner CommandRunner
	wait   func(context.Context, time.Duration) error
}

type boundedCommandRunner struct {
	executable string
	runner     *commandrunner.Runner
}

func (runner boundedCommandRunner) Run(ctx context.Context, executable string, args []string) ([]byte, []byte, error) {
	if executable != runner.executable {
		return nil, nil, errors.New("Xray command executable does not match configured client")
	}
	result, err := runner.runner.Run(ctx, args)
	return result.Stdout(), nil, err
}

func NewClient(binary, server string, runner CommandRunner) (*Client, error) {
	if !filepath.IsAbs(binary) {
		return nil, errors.New("Xray binary path must be absolute")
	}
	if server == "" {
		return nil, errors.New("Xray API server must not be empty")
	}
	if runner == nil {
		bounded, err := commandrunner.New(binary, commandTimeout)
		if err != nil {
			return nil, errors.New("create bounded Xray command runner")
		}
		runner = boundedCommandRunner{executable: binary, runner: bounded}
	}
	return &Client{
		binary: binary,
		server: server,
		runner: runner,
		wait:   waitContext,
	}, nil
}

func (client *Client) ApplyRouting(ctx context.Context, payload xrayconfig.RoutingPayload) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("Xray RoutingService adrules canceled: %w", err)
	}
	if !json.Valid(payload) {
		return errors.New("invalid Xray routing payload")
	}
	config, err := json.Marshal(struct {
		Routing xrayconfig.RoutingPayload `json:"routing"`
	}{Routing: payload})
	if err != nil {
		return errors.New("encode Xray routing payload")
	}

	file, err := os.CreateTemp("", "xray-routing-*.json")
	if err != nil {
		return errors.New("create temporary Xray routing payload")
	}
	path := file.Name()
	defer func() {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			resultErr = errors.Join(resultErr, errors.New("remove temporary Xray routing payload"))
		}
	}()

	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return errors.New("secure temporary Xray routing payload")
	}
	if _, err := file.Write(config); err != nil {
		_ = file.Close()
		return errors.New("write temporary Xray routing payload")
	}
	if err := file.Close(); err != nil {
		return errors.New("close temporary Xray routing payload")
	}

	args := []string{"api", "adrules", "--server=" + client.server, path}
	return client.run(ctx, "adrules", args)
}

func (client *Client) ListRules(ctx context.Context) ([]byte, error) {
	args := []string{"api", "lsrules", "--server=" + client.server}
	returnOutput, err := client.runOutput(ctx, "lsrules", args)
	return returnOutput, err
}

func (client *Client) run(ctx context.Context, operation string, args []string) error {
	_, err := client.runOutput(ctx, operation, args)
	return err
}

func (client *Client) runOutput(ctx context.Context, operation string, args []string) ([]byte, error) {
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("Xray RoutingService %s canceled: %w", operation, err)
		}
		stdout, _, err := client.runner.Run(ctx, client.binary, args)
		if err == nil {
			return stdout, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("Xray RoutingService %s canceled: %w", operation, ctxErr)
		}
		if attempt == maxAttempts {
			return nil, fmt.Errorf("Xray RoutingService %s failed", operation)
		}
		if err := client.wait(ctx, retryDelay); err != nil {
			return nil, fmt.Errorf("Xray RoutingService %s canceled: %w", operation, err)
		}
	}
	return nil, fmt.Errorf("Xray RoutingService %s failed", operation)
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
