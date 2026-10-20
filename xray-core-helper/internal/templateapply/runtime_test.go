package templateapply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/nativeblock"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type commandCall struct {
	executable string
	args       []string
}
type runnerStub struct {
	calls  []commandCall
	failAt int
}

func (stub *runnerStub) Run(_ context.Context, executable string, args []string) error {
	stub.calls = append(stub.calls, commandCall{executable, append([]string(nil), args...)})
	return nil
}

func TestRuntimeUsesOnlyFixedCommandsPerTemplate(t *testing.T) {
	tests := []struct {
		id   templatecatalog.ID
		path string
		want []commandCall
	}{
		{templatecatalog.XrayConfig, "/usr/local/etc/xray/config.json", []commandCall{{systemctlBinary, []string{"is-active", "--quiet", "xray.service"}}, {systemctlBinary, []string{"restart", "xray.service"}}, {systemctlBinary, []string{"is-active", "--quiet", "xray.service"}}}},
		{templatecatalog.TProxyUnit, "/etc/systemd/system/xray-tproxy-routing.service", []commandCall{{systemctlBinary, []string{"is-active", "--quiet", "xray-tproxy-routing.service"}}, {systemctlBinary, []string{"daemon-reload"}}, {systemctlBinary, []string{"restart", "xray-tproxy-routing.service"}}, {systemctlBinary, []string{"is-active", "--quiet", "xray-tproxy-routing.service"}}}},
		{templatecatalog.Sysctl, "/etc/sysctl.d/99-xray.conf", []commandCall{{systemctlBinary, []string{"start", "xray-core-helper-sysctl.service"}}}},
	}
	for _, test := range tests {
		t.Run(string(test.id), func(t *testing.T) {
			runner := &runnerStub{}
			runtime, _ := newCommandRuntime(runner)
			runtime.verifyAPI = func(context.Context, string) error { return nil }
			sources := map[templatecatalog.ID]string{templatecatalog.XrayConfig: "xray/config.json", templatecatalog.Sysctl: "sysctl/99-xray.conf", templatecatalog.TProxyUnit: "systemd/xray-tproxy-routing.service"}
			if source, ok := sources[test.id]; ok {
				raw, err := os.ReadFile(filepath.Join("../../templConf", source))
				if err != nil {
					t.Fatal(err)
				}
				if test.id == templatecatalog.XrayConfig {
					config := bootstrap.Defaults()
					config.Network.Interface, config.Network.Address, config.Network.IPv4Prefix = "br-test", "192.0.2.1", "192.0.2.0/24"
					raw, err = xrayconfig.RenderSetupTemplate(raw, nil, xrayconfig.TemplateValues{Network: config.Network, Logging: config.Logging, API: config.Xray.API, UserGeosite: config.Geodata.UserGeosite, UserGeoip: config.Geodata.UserGeoip})
					if err != nil {
						t.Fatal(err)
					}
				}
				test.path = filepath.Join(t.TempDir(), "candidate")
				if err = os.WriteFile(test.path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			transaction, err := runtime.Prepare(context.Background(), test.id)
			if err != nil {
				t.Fatal(err)
			}
			if err = transaction.Apply(context.Background(), test.path); err != nil {
				t.Fatal(err)
			}
			if err = transaction.Healthy(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(runner.calls, test.want) {
				t.Fatalf("calls=%#v want=%#v", runner.calls, test.want)
			}
		})
	}
}

func TestNftApplyUsesCheckedNativeBlockAndRollbackDoesNotReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.nft")
	os.WriteFile(path, []byte("# unrelated file text\n"+nativeblock.Begin+"\ntable ip custom {}\n"+nativeblock.End+"\n"), 0600)
	runner := &runnerStub{}
	runtime, _ := newCommandRuntime(runner)
	tx, _ := runtime.Prepare(context.Background(), templatecatalog.Nftables)
	for _, apply := range []func(context.Context, string) error{tx.Apply} {
		runner.calls = nil
		if err := apply(context.Background(), path); err != nil {
			t.Fatal(err)
		}
		if len(runner.calls) != 2 {
			t.Fatal(runner.calls)
		}
		check, load := runner.calls[0], runner.calls[1]
		if check.executable != nftBinary || !reflect.DeepEqual(check.args[:2], []string{"--check", "--file"}) || !reflect.DeepEqual(load.args, []string{"--file", check.args[2]}) {
			t.Fatal(runner.calls)
		}
		if _, err := os.Stat(check.args[2]); !os.IsNotExist(err) {
			t.Fatal("batch not cleaned")
		}
	}
}

func TestRuntimeNftRollbackNeverReloads(t *testing.T) {
	runner := &runnerStub{}
	runtime, _ := newCommandRuntime(runner)
	tx, _ := runtime.Prepare(context.Background(), templatecatalog.Nftables)
	if err := tx.Rollback(context.Background(), "/unused"); !IsCode(err, CodeSafeApply) || len(runner.calls) != 0 {
		t.Fatal("runtime rollback must require manual reboot", err)
	}
}

func TestRuntimeRejectsUnknownTemplateBeforeCommand(t *testing.T) {
	runner := &runnerStub{}
	runtime, _ := newCommandRuntime(runner)
	_, err := runtime.Prepare(context.Background(), "other")
	if !IsCode(err, CodeUnknownTemplate) {
		t.Fatalf("err=%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("calls=%v", runner.calls)
	}
}

func TestActivationGateStopsNetworkCommandsBeforeMutation(t *testing.T) {
	for _, id := range []templatecatalog.ID{templatecatalog.Nftables, templatecatalog.TProxyUnit} {
		runner := &runnerStub{}
		runtime, _ := newCommandRuntime(runner)
		runtime.gate = func(context.Context) error { return errors.New("not ready") }
		tx, err := runtime.Prepare(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		before := len(runner.calls)
		if tx.Apply(context.Background(), "/unused") == nil {
			t.Fatal("unready gateway accepted")
		}
		if len(runner.calls) != before {
			t.Fatal("commands executed before readiness")
		}
	}
}
