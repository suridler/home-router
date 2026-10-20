package templateapply

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/nativebinding"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
)

func TestExplicitCandidateBindsSavedNetworkAndCurrentXray(t *testing.T) {
	root := t.TempDir()
	xrayPath := filepath.Join(root, "xray.json")
	xray := []byte(`{"inbounds":[{"tag":"tproxy-in","port":61000}],"outbounds":[{"tag":"direct","protocol":"freedom","streamSettings":{"sockopt":{"mark":42}}}]}`)
	if err := os.WriteFile(xrayPath, xray, 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := templatecatalog.ForXrayConfig(xrayPath)
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{catalog: catalog}
	network := bootstrap.NetworkConfig{Interface: "br-lan", Address: "10.77.0.1", IPv4Prefix: "10.77.0.0/24", InterceptMark: 2, PolicyTable: 321}
	engine.SetNetwork(network)
	raw := []byte("# helper:begin gateway\n# helper:v1 bind ingress type=interface source=setup.interface\ndefine device = ${lan_interface}\n# helper:v1 bind listener type=port source=xray.tproxy-port\ndefine port = ${tproxy_port}\n# helper:v1 bind outgoing type=uint32 source=xray.egress-mark\ndefine bypass = ${egress_mark}\n# helper:v1 bind interception type=uint32 source=helper.network.intercept-mark\ndefine mark = ${intercept_mark}\n# helper:end gateway\n")
	working := filepath.Join(root, "working.nft")
	system := filepath.Join(root, "system.nft")
	original := []byte("# operator prefix\n# helper:begin gateway\nold content\n# helper:end gateway\n# operator suffix\n")
	os.WriteFile(working, raw, 0600)
	os.WriteFile(system, original, 0600)
	entry := templatecatalog.Entry{ID: templatecatalog.Nftables, WorkingPath: working, SystemPath: system}
	prepared, err := engine.candidate(context.Background(), entry, true)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.close()
	got, err := os.ReadFile(prepared.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`define device = "br-lan"`, `define port = 61000`, `define bypass = 42`, `define mark = 2`} {
		if !bytes.Contains(got, []byte(want)) {
			t.Fatal("source not bound", want)
		}
	}
	if !bytes.HasPrefix(got, []byte("# operator prefix\n")) || !bytes.HasSuffix(got, []byte("# operator suffix\n")) {
		t.Fatal("unmanaged nft bytes changed")
	}
	if _, err = nativebinding.Parse(got); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]byte{working: raw, system: original, xrayPath: xray} {
		actual, _ := os.ReadFile(path)
		if !bytes.Equal(actual, want) {
			t.Fatal("candidate modified source", path)
		}
	}
	engine.SetNetwork(bootstrap.Defaults().Network)
	if _, err = engine.candidate(context.Background(), entry, true); err == nil {
		t.Fatal("unselected interface accepted")
	}
}

func TestXrayCandidateRendersHelperValuesAndCrossChecksDrift(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	system := filepath.Join(root, "xray.json")
	working := filepath.Join(root, "xray.json.tmpl")
	current := []byte(`{"inbounds":[{"tag":"tproxy-in","port":61000}],"outbounds":[{"streamSettings":{"sockopt":{"mark":511}}}]}`)
	raw := []byte(`{
 // keep policy and comments
 "inbounds":[{"tag":"tproxy-in",
 // helper:v1 bind port type=port source=xray.tproxy-port
 "port": ${port}
 }],
 "outbounds":[{"streamSettings":{"sockopt":{
 // helper:v1 bind mark type=uint32 source=xray.egress-mark
 "mark": ${mark}
 }}}],
 "log":{
 // helper:v1 bind level type=log-level source=helper.logging.level
 "loglevel": ${level}
 },
 "user": [
 // helper:v1 bind geoip type=geoip-reference source=helper.user-geoip
 ${geoip}
 ]
}`)
	for path, content := range map[string][]byte{system: current, working: raw} {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := templatecatalog.ForXrayConfig(system)
	if err != nil {
		t.Fatal(err)
	}
	config := bootstrap.Defaults()
	values := xrayconfig.TemplateValues{Logging: config.Logging, API: config.Xray.API, UserGeosite: config.Geodata.UserGeosite, UserGeoip: config.Geodata.UserGeoip}
	engine := &Engine{catalog: catalog}
	engine.SetXrayValues(values)
	entry := templatecatalog.Entry{ID: templatecatalog.XrayConfig, WorkingPath: working, SystemPath: system}
	prepared, err := engine.candidate(ctx, entry, true)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.close()
	content, err := os.ReadFile(prepared.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"port": 61000`, `"mark": 511`, `"loglevel": "warning"`, `"ext:geoip_user.dat:USERLIST"`, "// keep policy and comments"} {
		if !bytes.Contains(content, []byte(want)) {
			t.Fatal("binding missing", want)
		}
	}
	checker, err := NewCrossCheckerWithCatalog(validatorStub{}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	// Related file lookups stay hermetic; only our temporary candidate exists.
	checker.read = func(path string) ([]byte, error) {
		if path != prepared.path {
			return nil, os.ErrNotExist
		}
		return os.ReadFile(path)
	}
	checker.SetXrayValues(values)
	if err := checker.Check(ctx, entry.ID, prepared.path); err != nil {
		t.Fatal(err)
	}
	values.Logging.Level = "error"
	checker.SetXrayValues(values)
	if err := checker.Check(ctx, entry.ID, prepared.path); !IsCode(err, CodeBindingMismatch) {
		t.Fatal("logging drift was not detected", err)
	}
	for path, expected := range map[string][]byte{system: current, working: raw} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, expected) {
			t.Fatal("render changed a source file", err)
		}
	}
}

func TestUnitFactoryUsesSavedMarkAndTableWithoutExpandingCommands(t *testing.T) {
	raw, err := os.ReadFile("../../templConf/systemd/xray-tproxy-routing.service")
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{}
	n := bootstrap.Defaults().Network
	n.InterceptMark = 2
	n.PolicyTable = 321
	engine.SetNetwork(n)
	got, err := engine.renderNative(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Environment=TPROXY_MARK=2", "Environment=TPROXY_TABLE=321", "ExecStart=/usr/local/libexec/xray-tproxy/network on"} {
		if !strings.Contains(string(got), want) {
			t.Fatal(want)
		}
	}
}
