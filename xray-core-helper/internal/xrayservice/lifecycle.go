package xrayservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

var ErrGatewayLifecycle = errors.New("xray_gateway_lifecycle_conflict")

// CheckGatewayLifecycle checks synchronous systemctl hooks for an opposing
// effective ordering edge. Hooks alone are valid: the native lifecycle uses
// them without an After=xray.service edge on the routing unit. Dependency
// propagation (BindsTo/PartOf) alone does not impose job ordering.
// It never rewrites administrator units or executes hook commands.
func (r *Resolver) CheckGatewayLifecycle(ctx context.Context) error {
	const routing = "xray-tproxy-routing.service"
	var startRouting, stopRouting bool
	for _, name := range []string{"ExecStartPre", "ExecStartPost", "ExecStop", "ExecStopPost"} {
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
			if len(argv) == 2 && argv[0] == "/usr/local/libexec/xray-tproxy/network" {
				if name == "ExecStartPost" && argv[1] == "xray-start" {
					startRouting = true
				}
				if (name == "ExecStop" || name == "ExecStopPost") && argv[1] == "xray-stop" {
					stopRouting = true
				}
				continue
			}
			if len(argv) == 0 || (argv[0] != "/usr/bin/systemctl" && argv[0] != "/bin/systemctl") {
				continue
			}
			asynchronous, target, action := false, false, ""
			for _, arg := range argv[1:] {
				switch arg {
				case "--no-block":
					asynchronous = true
				case routing:
					target = true
				case "start", "restart", "stop":
					action = arg
				}
			}
			if asynchronous || !target {
				continue
			}
			if (name == "ExecStartPre" || name == "ExecStartPost") && (action == "start" || action == "restart") {
				startRouting = true
			}
			if (name == "ExecStop" || name == "ExecStopPost") && (action == "stop" || action == "restart") {
				stopRouting = true
			}
		}
	}
	if !startRouting && !stopRouting {
		return nil
	}
	for _, unit := range []struct {
		path, blockedBy string
		needed          bool
	}{
		{"xray_2dtproxy_2drouting_2eservice", "xray.service", startRouting},
		{"xray_2eservice", routing, stopRouting},
	} {
		if !unit.needed {
			continue
		}
		var after []string
		args := []string{"get-property", "org.freedesktop.systemd1", "/org/freedesktop/systemd1/unit/" + unit.path, "org.freedesktop.systemd1.Unit", "After"}
		if r.query(ctx, args, "as", &after) != nil {
			return ErrGatewayLifecycle
		}
		for _, dependency := range after {
			if strings.TrimSpace(dependency) == unit.blockedBy {
				return ErrGatewayLifecycle
			}
		}
	}
	return nil
}
