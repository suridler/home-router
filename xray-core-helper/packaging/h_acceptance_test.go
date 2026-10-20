package packaging

import (
	"os"
	"strings"
	"testing"
)

func TestH1RequiresDisposableHostAndKeepsRoutingDisabled(t *testing.T) {
	raw, err := os.ReadFile("12h1-clean-host-smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{"XCH_DISPOSABLE_HOST", "cli|web", "configured_direct", "routingEnabled == false", "! systemctl is-active --quiet xray-tproxy-routing.service", "unexpected active inet xray table", "before.json", "discover-xray", "--adopt-existing", "previous helper installation found"} {
		if !strings.Contains(source, want) {
			t.Errorf("missing %q", want)
		}
	}
}
