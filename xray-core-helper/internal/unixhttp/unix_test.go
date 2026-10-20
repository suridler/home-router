package unixhttp

import (
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestListenAppliesConfiguredGroupAndMode(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "run", "helper.sock")
	var gotUID, gotGID int
	listener, err := listen(socketPath, "xray-core-helper", func(name string) (*user.Group, error) {
		if name != "xray-core-helper" {
			t.Fatalf("lookup group = %q", name)
		}
		return &user.Group{Gid: "4242"}, nil
	}, func(path string, uid, gid int) error {
		if path != socketPath {
			t.Fatalf("chown path = %q", path)
		}
		gotUID, gotGID = uid, gid
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if gotUID != -1 || gotGID != 4242 {
		t.Fatalf("chown IDs = %d:%d; want -1:4242", gotUID, gotGID)
	}
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0660 {
		t.Fatalf("socket mode = %04o; want 0660", got)
	}
}

func TestListenWithoutConfiguredGroup(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "helper.sock")
	listener, err := Listen(socketPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket remains after close: %v", err)
	}
}

func TestListenCleansUpAfterGroupFailure(t *testing.T) {
	tests := []struct {
		name   string
		lookup groupLookup
		chown  socketChown
		want   string
	}{
		{
			name: "lookup",
			lookup: func(string) (*user.Group, error) {
				return nil, errors.New("missing")
			},
			chown: func(string, int, int) error { return nil },
			want:  "resolve Unix socket group",
		},
		{
			name:   "invalid GID",
			lookup: func(string) (*user.Group, error) { return &user.Group{Gid: "bad"}, nil },
			chown:  func(string, int, int) error { return nil },
			want:   "invalid GID",
		},
		{
			name:   "chown",
			lookup: func(string) (*user.Group, error) { return &user.Group{Gid: "42"}, nil },
			chown:  func(string, int, int) error { return errors.New("denied") },
			want:   "set Unix socket group",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			socketPath := filepath.Join(t.TempDir(), "helper.sock")
			listener, err := listen(socketPath, "xray-core-helper", test.lookup, test.chown)
			if listener != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("listen() = %v, %v; want nil error containing %q", listener, err, test.want)
			}
			if _, statErr := os.Stat(socketPath); !os.IsNotExist(statErr) {
				t.Fatalf("socket remains after error: %v", statErr)
			}
			connection, dialErr := net.Dial("unix", socketPath)
			if dialErr == nil {
				_ = connection.Close()
				t.Fatal("listener remains reachable after error")
			}
		})
	}
}
