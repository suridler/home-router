package unixhttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"
)

type groupLookup func(string) (*user.Group, error)
type socketChown func(string, int, int) error

func Listen(socketPath, socketGroup string) (net.Listener, error) {
	return listen(socketPath, socketGroup, user.LookupGroup, os.Chown)
}

func listen(socketPath, socketGroup string, lookup groupLookup, chown socketChown) (net.Listener, error) {
	if !filepath.IsAbs(socketPath) {
		return nil, fmt.Errorf("Unix socket path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0750); err != nil {
		return nil, fmt.Errorf("create Unix socket directory: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on Unix socket: %w", err)
	}
	cleanup := func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}
	if socketGroup != "" {
		group, err := lookup(socketGroup)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("resolve Unix socket group: %w", err)
		}
		gid, err := strconv.Atoi(group.Gid)
		if err != nil || gid < 0 {
			cleanup()
			return nil, fmt.Errorf("resolve Unix socket group: invalid GID")
		}
		if err := chown(socketPath, -1, gid); err != nil {
			cleanup()
			return nil, fmt.Errorf("set Unix socket group: %w", err)
		}
	}
	if err := os.Chmod(socketPath, 0660); err != nil {
		cleanup()
		return nil, fmt.Errorf("set Unix socket permissions: %w", err)
	}
	return &removingListener{Listener: listener, path: socketPath}, nil
}

func NewClient(socketPath string, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

type removingListener struct {
	net.Listener
	path string
}

func (listener *removingListener) Close() error {
	closeErr := listener.Listener.Close()
	removeErr := os.Remove(listener.path)
	if os.IsNotExist(removeErr) {
		removeErr = nil
	}
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}
