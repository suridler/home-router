package routerdiagnostics

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestConnectivitySeparatesLayersAndNeverLeaksErrors(t *testing.T) {
	secret := errors.New("credential secret.example")
	for _, which := range []string{"dns", "outbound", "api"} {
		c := NewConnectivity(func(context.Context) error {
			if which == "api" {
				return secret
			}
			return nil
		}, func(context.Context, string) (string, error) { return "configured:443", nil })
		c.DNS = func(context.Context) error {
			if which == "dns" {
				return secret
			}
			return nil
		}
		c.Dial = func(context.Context, string) error {
			if which == "outbound" {
				return secret
			}
			return nil
		}
		result, err := c.Probe(context.Background(), "proxy")
		if err != nil {
			t.Fatal(err)
		}
		expected := map[string]int{"api": 0, "dns": 1, "outbound": 2}[which]
		for i, layer := range result.Layers[:3] {
			if (layer.Status == "failed") != (i == expected) || strings.Contains(layer.Code, "secret") {
				t.Fatal(result)
			}
			if layer.CheckedAt.IsZero() || layer.TimeoutMS != 3000 {
				t.Fatal(layer)
			}
		}
		if result.Layers[4].Status != "requires_client_test" {
			t.Fatal("local probe claimed LAN success")
		}
	}
}
