package httpapi

import (
	"net/http"

	"home-router/xray-core-helper/internal/api"
)

func registerWebSettings(mux *http.ServeMux, settings *api.WebSettingsResponse) {
	// Capture the startup snapshot; callers cannot mutate live API settings.
	var snapshot api.WebSettingsResponse
	if settings != nil {
		snapshot = *settings
	}
	mux.HandleFunc("/v1/web/settings", methods(map[string]http.HandlerFunc{http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
		if settings == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "web_settings_unavailable", "web settings unavailable")
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	}}))
}
