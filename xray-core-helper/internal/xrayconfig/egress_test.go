package xrayconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewOutboundInheritsNonDefaultEgressMark(t *testing.T) {
	source := strings.ReplaceAll(outboundFixture, `"mark": 255`, `"mark": 511`)
	updated, err := plan(t, PlanWriteOutbound("edge-2", json.RawMessage(nativeDraft), "create"), source)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseSource([]byte(updated))
	if err != nil {
		t.Fatal(err)
	}
	_, added := findOutbound(parsed, "edge-2")
	mark := asMap(asMap(added["streamSettings"])["sockopt"])["mark"]
	if n, ok := markInteger(mark); !ok || n != 511 {
		t.Fatal("default mark reintroduced")
	}
}
