package xrayconfig

import (
	"strings"
	"testing"
)

func TestExtractNativeRoutingRelationsIgnoresJSONCOrderAndComments(t *testing.T) {
	source := []byte("{\"inbounds\":[{\"port\":52345,// reordered\n\"tag\":\"tproxy-in\"}],\"outbounds\":[{\"streamSettings\":{\"sockopt\":{\"mark\":255}}}]}")
	port, err := BindingValue(source, "xray.tproxy-port")
	if err != nil {
		t.Fatal(err)
	}
	mark, err := BindingValue(source, "xray.egress-mark")
	if err != nil || port != "52345" || mark != "255" {
		t.Fatal("structured binding projection failed", err)
	}
}

func TestNativeMarksAreUint32NotPortsAndDuplicateInboundIsRejected(t *testing.T) {
	source := `{"inbounds":[{"tag":"tproxy-in","port":60000}],"outbounds":[{"streamSettings":{"sockopt":{"mark":65536}}}]}`
	if _, err := BindingValue([]byte(source), "xray.egress-mark"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.ReplaceAll(source, "65536", "4294967296"), strings.ReplaceAll(source, "65536", `"secret"`), strings.ReplaceAll(source, `"inbounds":[`, `"inbounds":[{"tag":"tproxy-in","port":1},`)} {
		provider := "xray.egress-mark"
		if strings.Contains(bad, `"port":1`) {
			provider = "xray.tproxy-port"
		}
		if _, err := BindingValue([]byte(bad), provider); err == nil {
			t.Fatal("ambiguous/invalid native relations accepted")
		}
	}
}
