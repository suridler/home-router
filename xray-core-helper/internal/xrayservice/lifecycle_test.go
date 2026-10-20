package xrayservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestGatewayLifecycleChecksConflictingOrderingNotHooksAlone(t *testing.T) {
	for _, tc := range []struct {
		name, hook, action      string
		routingAfter, xrayAfter []string
		async, wantConflict     bool
	}{
		{name: "no hooks"},
		{name: "native start", hook: "ExecStartPost", action: "start"},
		{name: "native stop", hook: "ExecStopPost", action: "stop"},
		{name: "pre-start cleanup", hook: "ExecStartPre", action: "stop", routingAfter: []string{"xray.service"}},
		{name: "start cycle", hook: "ExecStartPost", action: "start", routingAfter: []string{"xray.service"}, wantConflict: true},
		{name: "stop cycle", hook: "ExecStopPost", action: "stop", xrayAfter: []string{"xray-tproxy-routing.service"}, wantConflict: true},
		{name: "stop before process exit cycle", hook: "ExecStop", action: "stop", xrayAfter: []string{"xray-tproxy-routing.service"}, wantConflict: true},
		{name: "unrelated ordering", hook: "ExecStartPost", action: "start", routingAfter: []string{"network.target"}},
		{name: "asynchronous start has no wait cycle", hook: "ExecStartPost", action: "start", routingAfter: []string{"xray.service"}, async: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Resolver{run: func(_ context.Context, args []string) ([]byte, error) {
				if args[0] != "get-property" {
					t.Fatalf("unexpected mutation/query: %v", args)
				}
				if args[len(args)-1] == "After" {
					data := tc.xrayAfter
					if strings.Contains(args[2], "xray_2dtproxy") {
						data = tc.routingAfter
					}
					if data == nil {
						data = []string{}
					}
					return json.Marshal(map[string]any{"type": "as", "data": data})
				}
				commands := [][]any{}
				if args[len(args)-1] == tc.hook {
					argv := []string{"/usr/bin/systemctl", tc.action, "xray-tproxy-routing.service"}
					if tc.async {
						argv = append(argv, "--no-block")
					}
					commands = [][]any{{"/usr/bin/systemctl", argv, false, 0, 0, 0, 0, 0, 0, 0}}
				}
				return json.Marshal(map[string]any{"type": "a(sasbttttuii)", "data": commands})
			}}
			err := r.CheckGatewayLifecycle(context.Background())
			if errors.Is(err, ErrGatewayLifecycle) != tc.wantConflict {
				t.Fatalf("got %v; want conflict %v", err, tc.wantConflict)
			}
		})
	}
}

func TestGatewayLifecycleReportsUnreadablePropertiesWithoutCommandOutput(t *testing.T) {
	r := &Resolver{run: func(context.Context, []string) ([]byte, error) { return nil, errors.New("secret command output") }}
	if err := r.CheckGatewayLifecycle(context.Background()); err != ErrGatewayLifecycle {
		t.Fatal(err)
	}
}
