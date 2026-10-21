package xrayconfig

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/xtls/xray-core/infra/conf"
)

// Build with upstream Xray types as well as the helper planner. No network or host services.
func TestAllNativeOutboundTypesBuildAfterBinding(t *testing.T) {
	raw, err := os.ReadFile("../outbound/testdata/manual.json")
	if err != nil {
		t.Fatal(err)
	}
	var drafts []json.RawMessage
	if err := json.Unmarshal(raw, &drafts); err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 13 {
		t.Fatal("incomplete manual matrix")
	}
	for _, draft := range drafts {
		var identity struct{ Tag, Protocol string }
		_ = json.Unmarshal(draft, &identity)
		t.Run(identity.Protocol, func(t *testing.T) {
			updated, err := plan(t, PlanWriteOutbound(identity.Tag, draft, "create"), outboundFixture)
			if err != nil {
				t.Fatal(err)
			}
			_, value := findOutbound(newEditorT(t, updated).syntax, identity.Tag)
			raw, _ := json.Marshal(value)
			var native conf.OutboundDetourConfig
			if err := json.Unmarshal(raw, &native); err != nil {
				t.Fatal(err)
			}
			if _, err := native.Build(); err != nil {
				t.Fatalf("native build failed: %v", err)
			}
		})
	}
}
