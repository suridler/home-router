package setup

import (
	"context"
	"home-router/xray-core-helper/internal/templatecatalog"
	"os"
	"strings"
	"testing"
)

func TestSetupChecksAllRenderedNativeCandidatesBeforeCommit(t *testing.T) {
	for _, bad := range []bool{false, true} {
		t.Run(map[bool]string{false: "coherent", true: "port-mismatch"}[bad], func(t *testing.T) {
			s, entries, _ := fileStoreFixture(t)
			for _, entry := range entries {
				var raw []byte
				var err error
				switch entry.ID {
				case templatecatalog.Nftables:
					raw, err = os.ReadFile("../../templConf/nftables/nftables.conf")
					if bad {
						raw = []byte(strings.Replace(string(raw), "# helper:v1 bind listener type=port source=xray.tproxy-port\ndefine tproxy_port = ${tproxy_port}", "# helper:v1 bind listener type=port source=xray.tproxy-port token=\"52345\"\ndefine tproxy_port = 52345", 1))
					}

				case templatecatalog.TProxyUnit:
					raw, err = os.ReadFile("../../templConf/systemd/xray-tproxy-routing.service")
				case templatecatalog.XrayConfig:
					raw = []byte(`{"inbounds":[{"tag":"tproxy-in","port":52345}],"outbounds":[{"streamSettings":{"sockopt":{"mark":255}}}]}`)
					if bad {
						raw = []byte(strings.ReplaceAll(string(raw), "52345", "60000"))
					}
				default:
					raw = []byte("native\n")
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(entry.WorkingPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.identity = func(context.Context) (int, error) { return 4242, nil }
			s.checkRelations = true
			tx, err := s.Prepare(context.Background(), Plan{Interface: "fixture0", LANNetwork: "192.0.2.0/24"})
			if err != nil {
				t.Fatal(err)
			}
			err = tx.Validate(context.Background())
			if (err != nil) != bad {
				t.Fatalf("bad=%t err=%v", bad, err)
			}
			if bad && err.Error() != "native_binding_mismatch" {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(entries[0].SystemPath)
			if len(raw) != 0 {
				t.Fatal("validation wrote system config")
			}
		})
	}
}
