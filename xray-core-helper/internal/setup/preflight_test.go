package setup

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/xrayconfig"
	"os"
	"testing"
)

func TestPreflightRejectsBeforeReplacement(t *testing.T) {
	raw, err := os.ReadFile("../../templConf/xray/config.json")
	if err != nil {
		t.Fatal(err)
	}
	config := bootstrap.Defaults()
	config.Network.Interface, config.Network.Address, config.Network.IPv4Prefix = "lan0", "10.0.0.1", "10.0.0.0/24"
	raw, err = xrayconfig.RenderSetupTemplate(raw, nil, setupTemplateValues(config))
	if err != nil {
		t.Fatal(err)
	}
	bad := errors.New("secret raw command output")
	for _, failure := range []string{"binary", "capability", "asset", "listener", "none"} {
		t.Run(failure, func(t *testing.T) {
			p := &preflight{binary: func(string) error {
				if failure == "binary" {
					return bad
				}
				return nil
			}, capability: func(context.Context) error {
				if failure == "capability" {
					return bad
				}
				return nil
			}, asset: func(string) error {
				if failure == "asset" {
					return bad
				}
				return nil
			}, listener: func(context.Context, string, string) error {
				if failure == "listener" {
					return bad
				}
				return nil
			}}
			err := p.check(context.Background(), Plan{ListenAddress: "10.0.0.1"}, raw)
			if failure == "none" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var typed *PreflightError
			if !errors.As(err, &typed) || typed.Code == "" {
				t.Fatal(err)
			}
		})
	}
}
func TestEnginePreflightFailureNeverPrepares(t *testing.T) {
	engine, calls := engineFixture(t, []Interface{{Name: "lan0", Addresses: []string{"10.0.0.1/24"}}}, "")
	engine.store = preflightFailStore{storeFake{calls: calls}}
	plan, _ := engine.Plan(context.Background(), "")
	if _, err := engine.Apply(context.Background(), plan); err == nil {
		t.Fatal("accepted failure")
	}
	if len(*calls) != 0 {
		t.Fatal(*calls)
	}
}

type preflightFailStore struct{ storeFake }

func (preflightFailStore) Preflight(context.Context, Plan) error {
	return &PreflightError{Code: "preflight_asset_unavailable"}
}
