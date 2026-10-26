package helperapp

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestNotifySystemdReady(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NOTIFY_SOCKET", path)
	if err = notifySystemdReady(); err != nil {
		t.Fatal(err)
	}
	if err = listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	size, _, err := listener.ReadFromUnix(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:size]) != "READY=1" {
		t.Fatalf("notification = %q", buffer[:size])
	}
}

func TestNotifySystemdReadyAllowsNonSystemdRun(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := notifySystemdReady(); err != nil {
		t.Fatal(err)
	}
}

func TestNotifySystemdReadyRejectsInvalidSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "relative.sock")
	if err := notifySystemdReady(); err == nil {
		t.Fatal("relative notification socket accepted")
	}
}
