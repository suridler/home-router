package gatewaycheck

import (
	"errors"
	"fmt"
	"testing"
)

func TestSocketEvidenceRequiresOwnedWildcardPortAndCorrectState(t *testing.T) {
	for _, tc := range []struct {
		network, address, state, inode string
		want                           bool
	}{
		{"tcp", "00000000:CC79", "0A", "123", true},
		{"udp", "00000000:CC79", "07", "123", true},
		{"tcp", "00000000000000000000000000000000:CC79", "0A", "123", true},
		{"tcp", "0100007F:CC79", "0A", "123", false},
		{"tcp", "00000000:CC78", "0A", "123", false},
		{"tcp", "00000000:CC79", "01", "123", false},
		{"udp", "00000000:CC79", "01", "123", false},
		{"tcp", "00000000:CC79", "0A", "456", false},
	} {
		raw := []byte(fmt.Sprintf("0: %s 00000000:0000 %s 0 0 0 0 0 %s", tc.address, tc.state, tc.inode))
		if ownsListener(raw, map[string]bool{"123": true}, 52345, tc.network) != tc.want {
			t.Fatal(tc)
		}
	}
	if ownsListener([]byte("malformed\n"), nil, 52345, "tcp") {
		t.Fatal("malformed table accepted")
	}
}

func TestDiagnosticStagePreservesNotReadyIdentity(t *testing.T) {
	err := stageError("api_unavailable")
	if !errors.Is(err, ErrNotReady) {
		t.Fatal("diagnostic lost error identity", err)
	}
	if err.Error() != "xray_gateway_not_ready: api_unavailable" {
		t.Fatal(err)
	}
}
