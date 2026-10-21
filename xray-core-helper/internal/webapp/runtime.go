// Package webapp prepares bootstrap data for the optional, systemd-owned GUI.
package webapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"home-router/xray-core-helper/internal/commandrunner"

	"home-router/xray-core-helper/internal/atomicfile"
)

const EnvironmentPath = "/run/xray-core-helper/web.env"
const TokenPath = "/var/lib/xray-core-helper/web/bootstrap-token"
const unit = "xrayui-web.service"

var ErrUnavailable = errors.New("web runtime unavailable")

func Enabled(value string) (bool, error) {
	switch value {
	case "", "1":
		return true, nil
	case "0":
		return false, nil
	default:
		return false, errors.New("XRAY_HELPER_WEB_ENABLED must be 0 or 1")
	}
}

type runner interface {
	Run(context.Context, []string) ([]byte, error)
}
type bounded struct{ command *commandrunner.Runner }

func (b bounded) Run(ctx context.Context, args []string) ([]byte, error) {
	result, err := b.command.Run(ctx, args)
	return result.Stdout(), err
}

type Runtime struct {
	enabled                    bool
	socket, envPath, tokenPath string
	runner                     runner
	group                      func() (int, error)
	dependencies               func() error
}

func New(socket string, enabled bool) (*Runtime, error) {
	if !filepath.IsAbs(socket) || strings.IndexFunc(socket, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return nil, ErrUnavailable
	}
	command, err := commandrunner.New("/usr/bin/systemctl", 15*time.Second)
	if err != nil {
		return nil, ErrUnavailable
	}
	return &Runtime{enabled: enabled, socket: socket, envPath: EnvironmentPath, tokenPath: TokenPath, runner: bounded{command}, group: func() (int, error) {
		group, err := user.LookupGroup("xrayui-web")
		if err != nil {
			return 0, ErrUnavailable
		}
		return strconv.Atoi(group.Gid)
	}, dependencies: func() error {
		for _, path := range []string{"/usr/bin/node", "/opt/xrayui-web/server.js"} {
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				return ErrUnavailable
			}
			if path == "/usr/bin/node" && info.Mode().Perm()&0111 == 0 {
				return ErrUnavailable
			}
		}
		return nil
	}}, nil
}

// Prepare never reads managed web settings from an environment file. The only
// generated values are the helper socket and (during setup/recovery) a token.
func (r *Runtime) Prepare(ctx context.Context, setupRequired bool) error {
	if !r.enabled {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.dependencies() != nil {
		return ErrUnavailable
	}
	gid, err := r.group()
	if err != nil {
		return ErrUnavailable
	}
	text := "XRAY_CORE_HELPER_SOCKET=" + strconv.Quote(r.socket) + "\n"
	token, err := readToken(r.tokenPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	if setupRequired {
		if errors.Is(err, os.ErrNotExist) {
			raw := make([]byte, 32)
			if _, err = rand.Read(raw); err != nil {
				return ErrUnavailable
			}
			token = hex.EncodeToString(raw)
			if err = write(r.tokenPath, []byte(token), 0600, os.Getegid()); err != nil {
				return ErrUnavailable
			}
		}
		text += "XRAYUI_BOOTSTRAP_TOKEN=" + token + "\n"
	} else if err == nil {
		if os.Remove(r.tokenPath) != nil || syncDir(filepath.Dir(r.tokenPath)) != nil {
			return ErrUnavailable
		}
	}
	if err = write(r.envPath, []byte(text), 0640, gid); err != nil {
		return ErrUnavailable
	}
	return nil
}

// Start queues only a fixed systemd job. --no-block avoids an ordering deadlock
// with helper.service; it is called after the Unix listener has been opened.
// There is deliberately no Stop-on-helper-shutdown hook.
func (r *Runtime) Reconcile(ctx context.Context) error {
	if r.enabled {
		if _, err := r.runner.Run(ctx, []string{"restart", "--no-block", unit}); err != nil {
			return ErrUnavailable
		}
		return nil
	}
	output, err := r.runner.Run(ctx, []string{"show", "--property=LoadState", "--value", unit})
	if strings.TrimSpace(string(output)) == "not-found" {
		return nil
	}
	if err != nil {
		return ErrUnavailable
	}
	if _, err = r.runner.Run(ctx, []string{"stop", unit}); err != nil {
		return ErrUnavailable
	}
	return nil
}

func safePath(path string) error {
	for current := path; current != "/"; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return ErrUnavailable
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
func readToken(path string) (string, error) {
	if err := safePath(path); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || !info.Mode().IsRegular() || info.Size() != 64 || info.Mode().Perm() != 0600 {
		return "", ErrUnavailable
	}
	raw := make([]byte, 64)
	if _, err = io.ReadFull(f, raw); err != nil {
		return "", ErrUnavailable
	}
	if _, err = hex.DecodeString(string(raw)); err != nil {
		return "", ErrUnavailable
	}
	return string(raw), nil
}
func write(path string, raw []byte, mode os.FileMode, gid int) error {
	if err := safePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// Persist the newly created web directory in its existing state/runtime
	// parent before publishing the token/env inside it.
	if err := syncDir(filepath.Dir(filepath.Dir(path))); err != nil {
		return err
	}
	_, err := atomicfile.Write(path, raw, func(f *os.File) error {
		if err := f.Chown(os.Geteuid(), gid); err != nil {
			return err
		}
		return f.Chmod(mode)
	})
	return err
}

func syncDir(path string) error { return atomicfile.SyncDir(path) }
