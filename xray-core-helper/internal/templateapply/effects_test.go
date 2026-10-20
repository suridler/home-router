package templateapply

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/templatecatalog"
	"os"
	"path/filepath"
	"testing"
)

type wrongEffectRunner struct{ runnerStub }

func (*wrongEffectRunner) Output(context.Context, string, []string) ([]byte, error) {
	return []byte("wrong"), nil
}
func TestFakeActiveIsNotHealthy(t *testing.T) {
	for id, source := range map[templatecatalog.ID]string{templatecatalog.XrayConfig: "xray/config.json"} {
		t.Run(string(id), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("../../templConf", source))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "native")
			os.WriteFile(path, raw, 0600)
			tx := &commandRuntimeTransaction{id: id, runner: &wrongEffectRunner{}, path: path, verifyAPI: func(context.Context, string) error { return errors.New("unready") }}
			if err = tx.Healthy(context.Background()); err == nil {
				t.Fatal("fake active accepted")
			}
		})
	}
}

func TestNftAndUnitEffectsAreUncheckedWithoutPolicyInference(t *testing.T) {
	for _, id := range []templatecatalog.ID{templatecatalog.Nftables, templatecatalog.TProxyUnit, templatecatalog.Sysctl} {
		tx := &commandRuntimeTransaction{id: id, runner: &wrongEffectRunner{}}
		if err := tx.Healthy(context.Background()); err != nil {
			t.Fatal("implicit native rule check", err)
		}
	}
}
