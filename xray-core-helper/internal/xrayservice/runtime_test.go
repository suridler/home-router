package xrayservice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestRuntimeIdentityRequiresActiveServiceAndValidInvocation(t *testing.T) {
	for _, tt := range []struct {
		name, state string
		id          []int
		fail        bool
	}{
		{"active", "active", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, false},
		{"inactive", "inactive", []int{1}, true},
		{"starting", "activating", []int{1}, true},
		{"short", "active", []int{1}, true},
		{"zero", "active", make([]int, 16), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &Resolver{run: func(_ context.Context, args []string) ([]byte, error) {
				if len(args) != 5 || args[2] != "/org/freedesktop/systemd1/unit/xray_2eservice" || args[3] != "org.freedesktop.systemd1.Unit" {
					t.Fatal("untrusted query", args)
				}
				if args[4] == "ActiveState" {
					return json.Marshal(map[string]any{"type": "s", "data": tt.state})
				}
				if args[4] != "InvocationID" {
					t.Fatal("unexpected property")
				}
				return json.Marshal(map[string]any{"type": "ay", "data": tt.id})
			}}
			id, err := resolver.RuntimeIdentity(context.Background())
			if (err != nil) != tt.fail {
				t.Fatal(id, err)
			}
			if tt.fail && !errors.Is(err, ErrRuntime) {
				t.Fatal("unredacted error", err)
			}
			if !tt.fail && id != "0102030405060708090a0b0c0d0e0f10" {
				t.Fatal("bad identity", id)
			}
		})
	}
}
