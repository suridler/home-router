package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/xrayconfig"
)

func TestWebSettingsInNormalAndSetupModes(t *testing.T) {
	for _, control := range []bool{false, true} {
		settings := api.WebSettingsResponse{Enabled: control, Host: "192.0.2.1", Port: 3001, ControlEnabled: control}
		want := settings
		servers := map[string]*http.Server{
			"normal":   NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{Web: &settings}),
			"setup":    NewSetupServer("test", setupFake{}, settings),
			"recovery": NewSetupServer("test", blockedSetupFake{}, settings),
		}
		settings.Port = 9999 // API exposes an immutable startup snapshot.
		for name, server := range servers {
			r := httptest.NewRecorder()
			server.Handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/v1/web/settings", nil))
			var got api.WebSettingsResponse
			if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil || r.Code != 200 || got != want {
				t.Fatalf("%s: %d %s (%v)", name, r.Code, r.Body.String(), err)
			}
			var fields map[string]any
			if err := json.Unmarshal(r.Body.Bytes(), &fields); err != nil || len(fields) != 5 {
				t.Fatalf("unexpected fields: %s", r.Body.String())
			}
			for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
				r := httptest.NewRecorder()
				server.Handler.ServeHTTP(r, httptest.NewRequest(method, "/v1/web/settings", nil))
				if r.Code != http.StatusMethodNotAllowed {
					t.Fatalf("%s %s: %d", name, method, r.Code)
				}
			}
		}
	}
}

func TestWebSettingsUnavailableWithoutConfiguration(t *testing.T) {
	for _, server := range []*http.Server{
		NewSetupServer("test", setupFake{}),
		NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil),
	} {
		r := httptest.NewRecorder()
		server.Handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/v1/web/settings", nil))
		var got api.ErrorResponse
		if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil || r.Code != 503 {
			t.Fatalf("%d %s (%v)", r.Code, r.Body.String(), err)
		}
	}
}
