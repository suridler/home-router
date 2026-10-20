package helperapp

import (
	"testing"

	"home-router/xray-core-helper/internal/bootstrap"
)

func TestWebSettingsProjectionPreservesControlPolicy(t *testing.T) {
	for _, control := range []bool{false, true} {
		config := bootstrap.WebConfig{Host: "192.0.2.1", Port: 3001, ControlEnabled: control}
		got := webSettingsResponse(config, false)
		if got.Enabled || got.Host != config.Host || got.Port != config.Port || got.ControlEnabled != control {
			t.Fatalf("projection = %#v", got)
		}
	}
}
