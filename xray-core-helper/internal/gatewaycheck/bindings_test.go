package gatewaycheck

import (
	"context"
	_ "home-router/xray-core-helper/internal/testconfig"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/nativebinding"
)

func TestExplicitDiagnosticUsesConfiguredNetworkAndRejectsDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.toml")
	if err := os.WriteFile(path, []byte("[network]\ninterface = 'br-lan'\naddress = '10.0.0.1'\nipv4_prefix = '10.0.0.0/24'\nintercept_mark = 2\npolicy_table = 321\n"), 0600); err != nil {
		t.Fatal(err)
	}
	docs := map[string]*nativebinding.Document{}
	for name, source := range map[string]string{
		"nftables":    "# helper:v1 bind intercept type=uint32 source=helper.network.intercept-mark token=\"2\"\ndefine mark = 2\n",
		"tproxy-unit": "# helper:v1 bind table type=uint32 source=helper.network.policy-table token=\"321\"\nEnvironment=TABLE=321\n",
	} {
		doc, err := nativebinding.Parse([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		docs[name] = doc
	}
	lan, err := nativebinding.Parse([]byte("# helper:v1 bind ingress type=interface source=helper.network.interface token=\"\\\"br-lan\\\"\"\ndefine device = \"br-lan\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	docs["lan"] = lan
	identity := func(context.Context) (int, error) { t.Fatal("undeclared service identity read"); return 0, nil }
	if err := checkDeclarations(context.Background(), docs, nil, path, identity); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[network]\nintercept_mark = 1\npolicy_table = 100\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkDeclarations(context.Background(), docs, nil, path, identity); err == nil {
		t.Fatal("native/network drift accepted during diagnostics")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := checkDeclarations(context.Background(), docs, nil, path, identity); err == nil {
		t.Fatal("missing config silently defaulted during diagnostics")
	}
}
