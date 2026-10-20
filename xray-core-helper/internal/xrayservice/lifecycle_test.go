package xrayservice

import (
	"context"
	"encoding/json"
	"testing"
)

func TestLegacyRoutingHooksAreRejectedWithoutExecutingThem(t *testing.T) {
	for _, hook := range []string{"", "ExecStartPre", "ExecStartPost", "ExecStopPost"} {
		r := &Resolver{run: func(_ context.Context, args []string) ([]byte, error) {
			commands := [][]any{}
			if args[len(args)-1] == hook {
				commands = [][]any{{"/usr/bin/systemctl", []string{"/usr/bin/systemctl", "start", "xray-tproxy-routing.service"}, false, 0, 0, 0, 0, 0, 0, 0}}
			}
			return json.Marshal(map[string]any{"type": "a(sasbttttuii)", "data": commands})
		}}
		if err := r.CheckGatewayLifecycle(context.Background()); (err != nil) != (hook != "") {
			t.Fatal(hook, err)
		}
	}
}
