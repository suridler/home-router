package xrayservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

var ErrGatewayLifecycle = errors.New("xray_gateway_lifecycle_conflict")

// CheckGatewayLifecycle detects the legacy synchronous service-control hooks
// which would deadlock with After/BindsTo/PartOf routing-unit dependencies.
// It never rewrites administrator units or executes their hook commands.
func (r *Resolver) CheckGatewayLifecycle(ctx context.Context) error {
	for _, name := range []string{"ExecStartPre", "ExecStartPost", "ExecStopPost"} {
		var commands [][]json.RawMessage
		args := []string{"get-property", "org.freedesktop.systemd1", "/org/freedesktop/systemd1/unit/xray_2eservice", "org.freedesktop.systemd1.Service", name}
		if r.query(ctx, args, "a(sasbttttuii)", &commands) != nil {
			return ErrGatewayLifecycle
		}
		for _, command := range commands {
			if len(command) != 10 {
				return ErrGatewayLifecycle
			}
			var argv []string
			if json.Unmarshal(command[1], &argv) != nil {
				return ErrGatewayLifecycle
			}
			for _, arg := range argv {
				if strings.Contains(arg, "xray-tproxy-routing") {
					return ErrGatewayLifecycle
				}
			}
		}
	}
	return nil
}
