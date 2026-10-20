// Package logpathcheck inspects service restrictions without changing them.
package logpathcheck

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/commandrunner"
)

// Result distinguishes paths helper cannot prepare from paths Xray cannot use.
// Unknown restrictions require acknowledgement as well; they are not proof of access.
type Result struct {
	HelperAccess, HelperError bool
	XrayAccess, XrayError     bool
}

func (r Result) Restricted() bool {
	return r.HelperAccess || r.HelperError || r.XrayAccess || r.XrayError
}

type Checker struct {
	show func(context.Context, string) ([]byte, error)
}

var properties = []string{"ProtectSystem", "ProtectHome", "ReadWritePaths", "ReadOnlyPaths", "InaccessiblePaths", "BindReadOnlyPaths", "TemporaryFileSystem", "RootDirectory", "RootImage", "PrivateTmp"}

func New() *Checker {
	return &Checker{show: func(ctx context.Context, unit string) ([]byte, error) {
		runner, err := commandrunner.New("/usr/bin/systemctl", 10*time.Second)
		if err != nil {
			return nil, err
		}
		result, err := runner.Run(ctx, []string{"show", "--property=" + strings.Join(properties, ","), unit})
		return result.Stdout(), err
	}}
}

func (c *Checker) Check(ctx context.Context, values bootstrap.LoggingConfig) (Result, error) {
	var result Result
	if ctx == nil || ctx.Err() != nil {
		return result, errors.New("log path inspection canceled")
	}
	if !filePath(values.AccessPath) && !filePath(values.ErrorPath) {
		return result, nil
	}
	for _, unit := range []string{"xray-core-helper.service", "xray.service"} {
		raw, err := c.show(ctx, unit)
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		p, valid := parse(raw)
		access := filePath(values.AccessPath) && (err != nil || !valid || restricted(values.AccessPath, p))
		errorLog := filePath(values.ErrorPath) && (err != nil || !valid || restricted(values.ErrorPath, p))
		if unit == "xray.service" {
			result.XrayAccess, result.XrayError = access, errorLog
		} else {
			result.HelperAccess, result.HelperError = access, errorLog
		}
	}
	return result, nil
}

func filePath(value string) bool { return value != "" && value != "none" }

func parse(raw []byte) (map[string]string, bool) {
	if len(raw) > 64<<10 {
		return nil, false
	}
	p := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, false
		}
		if _, exists := p[key]; exists {
			return nil, false
		}
		p[key] = value
	}
	for _, key := range properties {
		if _, ok := p[key]; !ok {
			return nil, false
		}
	}
	return p, true
}

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// Lists emitted in unfamiliar syntax are deliberately treated as unknown.
// This is a warning check, not a general systemd mount-namespace emulator.
func paths(raw string) ([]string, bool) {
	var result []string
	for _, value := range strings.Fields(raw) {
		value = strings.TrimPrefix(value, "-")
		if !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "\\\"'%:+") {
			return nil, false
		}
		result = append(result, value)
	}
	return result, true
}

func restricted(path string, p map[string]string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return true
	}
	for _, key := range []string{"BindReadOnlyPaths", "TemporaryFileSystem", "RootDirectory", "RootImage"} {
		if p[key] != "" {
			return true
		}
	}
	if p["PrivateTmp"] != "no" && (within(path, "/tmp") || within(path, "/var/tmp")) {
		return true
	}
	if p["ProtectHome"] != "no" && (within(path, "/home") || within(path, "/root") || within(path, "/run/user")) {
		return true
	}
	inaccessible, ok := paths(p["InaccessiblePaths"])
	if !ok {
		return true
	}
	for _, dir := range inaccessible {
		if within(path, dir) {
			return true
		}
	}
	// Most specific writable/read-only mount wins; equal paths remain read-only.
	readOnlyDepth, writableDepth := -1, -1
	switch p["ProtectSystem"] {
	case "strict":
		readOnlyDepth = 1
	case "full":
		if within(path, "/etc") {
			readOnlyDepth = len("/etc")
		}
		fallthrough
	case "yes":
		for _, dir := range []string{"/usr", "/boot", "/efi"} {
			if within(path, dir) {
				readOnlyDepth = len(dir)
			}
		}
	case "no":
	default:
		return true
	}
	for _, key := range []string{"ReadOnlyPaths", "ReadWritePaths"} {
		list, ok := paths(p[key])
		if !ok {
			return true
		}
		for _, dir := range list {
			if !within(path, dir) {
				continue
			}
			if key == "ReadOnlyPaths" && len(dir) > readOnlyDepth {
				readOnlyDepth = len(dir)
			}
			if key == "ReadWritePaths" && len(dir) > writableDepth {
				writableDepth = len(dir)
			}
		}
	}
	return readOnlyDepth >= 0 && writableDepth <= readOnlyDepth
}
