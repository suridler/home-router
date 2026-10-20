// Package xrayservice discovers the single configuration owned by xray.service.
package xrayservice

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"home-router/xray-core-helper/internal/commandrunner"
)

var ErrConfig = errors.New("xray_service_config_unavailable: require a direct Xray ExecStart with one absolute -config/-c file and no -confdir")

type Resolver struct {
	run func(context.Context, []string) ([]byte, error)
}

func New() *Resolver {
	return &Resolver{run: func(ctx context.Context, args []string) ([]byte, error) {
		runner, err := commandrunner.New("/usr/bin/busctl", 10*time.Second)
		if err != nil {
			return nil, ErrConfig
		}
		result, err := runner.Run(ctx, append([]string{"--system", "--json=short"}, args...))
		return result.Stdout(), err
	}}
}

type reply struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func (r *Resolver) query(ctx context.Context, args []string, signature string, destination any) error {
	raw, err := r.run(ctx, args)
	if err != nil || len(raw) > 64<<10 {
		return ErrConfig
	}
	var value reply
	if json.Unmarshal(raw, &value) != nil || value.Type != signature || json.Unmarshal(value.Data, destination) != nil {
		return ErrConfig
	}
	return nil
}

// ConfigPath reads systemd's effective service properties, including drop-ins.
// It neither starts Xray nor reads or logs configuration contents.
func (r *Resolver) ConfigPath(ctx context.Context, binary string) (string, error) {
	var objects []string
	if r.query(ctx, []string{"call", "org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager", "LoadUnit", "s", "xray.service"}, "o", &objects) != nil || len(objects) != 1 || objects[0] != "/org/freedesktop/systemd1/unit/xray_2eservice" {
		return "", ErrConfig
	}
	property := func(name string) []string {
		return []string{"get-property", "org.freedesktop.systemd1", objects[0], "org.freedesktop.systemd1.Service", name}
	}
	// Host paths cannot be inferred from a chroot or image namespace.
	for _, name := range []string{"RootDirectory", "RootImage"} {
		var value string
		if r.query(ctx, property(name), "s", &value) != nil || value != "" {
			return "", ErrConfig
		}
	}
	var commands [][]json.RawMessage
	if r.query(ctx, property("ExecStart"), "a(sasbttttuii)", &commands) != nil || len(commands) != 1 || len(commands[0]) != 10 {
		return "", ErrConfig
	}
	var executable string
	var argv []string
	if json.Unmarshal(commands[0][0], &executable) != nil || (binary != "" && executable != binary) || json.Unmarshal(commands[0][1], &argv) != nil || len(argv) < 2 || argv[0] != executable {
		return "", ErrConfig
	}
	return configArgument(argv[1:])
}

func configArgument(args []string) (string, error) {
	path := ""
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if strings.ContainsAny(arg, "$%\x00\r\n") {
			return "", ErrConfig
		}
		if arg == "run" && index == 0 {
			continue
		}
		key, value, inline := strings.Cut(arg, "=")
		switch key {
		case "-config", "--config", "-c", "--c":
			if path != "" {
				return "", ErrConfig
			}
			if !inline {
				index++
				if index >= len(args) {
					return "", ErrConfig
				}
				value = args[index]
			}
			if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" || strings.ContainsAny(value, "$%\x00\r\n") {
				return "", ErrConfig
			}
			path = value
		default:
			// Other inputs, stdin, directories, wrappers and implicit defaults
			// cannot identify one safe application-owned file.
			return "", ErrConfig
		}
	}
	if path == "" {
		return "", ErrConfig
	}
	return path, nil
}
