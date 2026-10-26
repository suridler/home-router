package helperapp

import (
	"errors"
	"net"
	"os"
	"strings"
)

// notifySystemdReady implements the small sd_notify subset needed by the
// helper. An empty NOTIFY_SOCKET keeps direct development runs unchanged.
func notifySystemdReady() error {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return nil
	}
	if strings.IndexByte(socket, 0) >= 0 || (socket[0] != '/' && socket[0] != '@') {
		return errors.New("invalid systemd notification socket")
	}
	connection, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err = connection.Write([]byte("READY=1")); err != nil {
		return err
	}
	return nil
}
