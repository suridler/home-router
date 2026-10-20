package setup

import (
	"context"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/nativeblock"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestNativeGatewayNetworkNamespace(t *testing.T) {
	if os.Getenv("XCH_DISPOSABLE_HOST") != "YES" || os.Getenv("XCH_NETNS_TEST") != "YES" {
		t.Skip("opt-in disposable-host network/mount namespace fixture")
	}
	if os.Geteuid() != 0 {
		t.Fatal("namespace fixture requires root in a disposable host")
	}
	for _, binary := range []string{"unshare", "ip", "nft", "jq", "ss", "mount", "bash", "setpriv", "python3", "/usr/local/bin/xray"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Fatalf("fixture dependency unavailable: %s", binary)
		}
	}
	raw, err := os.ReadFile("../../templConf/nftables/nftables.conf")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name                      string
		port, mark, bypass, table int
	}{{"defaults", 52345, 1, 255, 100}, {"alternatives", 60000, 2, 511, 321}} {
		t.Run(test.name, func(t *testing.T) {
			rendered, err := renderNative(templatecatalog.Nftables, raw, Plan{Interface: "lan0", LANNetwork: "10.77.0.0/24", xrayGID: 65534, Gateway: &xrayconfig.Gateway{Port: test.port, EgressMark: test.bypass}, network: bootstrap.NetworkConfig{InterceptMark: uint32(test.mark), PolicyTable: uint32(test.table)}})
			if err != nil {
				t.Fatal(err)
			}
			batch, err := nativeblock.Build(nil, rendered, true)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "batch.nft")
			if err = os.WriteFile(path, batch.Content, 0600); err != nil {
				t.Fatal(err)
			}
			script, err := filepath.Abs("../../packaging/r6-netns-fixture.sh")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			command := exec.CommandContext(ctx, "unshare", "--mount", "--net", "--propagation", "private", "--", "bash", script, path)
			command.Env = append(os.Environ(), "XCH_NETNS_CHILD=YES", "XCH_TEST_PORT="+strconv.Itoa(test.port), "XCH_TEST_MARK="+strconv.Itoa(test.mark), "XCH_TEST_BYPASS="+strconv.Itoa(test.bypass), "XCH_TEST_TABLE="+strconv.Itoa(test.table))
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("isolated native routing fixture failed: %v\\n%s", err, output)
			}
		})
	}
}
