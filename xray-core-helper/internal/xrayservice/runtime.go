package xrayservice

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
)

var ErrRuntime = errors.New("xray_runtime_identity_unavailable")

// RuntimeIdentity identifies the current active invocation, not just a reused PID.
// It never starts Xray or reads its configuration.
func (r *Resolver) RuntimeIdentity(ctx context.Context) (string, error) {
	property := func(name string) []string {
		return []string{"get-property", "org.freedesktop.systemd1", "/org/freedesktop/systemd1/unit/xray_2eservice", "org.freedesktop.systemd1.Unit", name}
	}
	var active string
	if r.query(ctx, property("ActiveState"), "s", &active) != nil || active != "active" {
		return "", ErrRuntime
	}
	var id []byte
	if r.query(ctx, property("InvocationID"), "ay", &id) != nil || len(id) != 16 || bytes.Equal(id, make([]byte, 16)) {
		return "", ErrRuntime
	}
	return hex.EncodeToString(id), nil
}
