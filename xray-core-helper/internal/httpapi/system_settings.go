package httpapi

import (
	"context"
	"errors"
	"net/http"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/systemsettings"
)

type SystemSettingsService interface {
	Get(context.Context) api.SystemSettingsResponse
	Put(context.Context, api.UpdateSystemSettingsRequest) (api.SystemSettingsResponse, error)
}

func registerSystemSettings(mux *http.ServeMux, service SystemSettingsService) {
	mux.HandleFunc("/v1/settings", methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			if service == nil {
				writeAPIError(w, http.StatusServiceUnavailable, "system_settings_unavailable", "system settings are unavailable")
				return
			}
			writeJSON(w, http.StatusOK, service.Get(r.Context()))
		},
		http.MethodPut: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.UpdateSystemSettingsRequest](w, r)
			if !ok {
				return
			}
			if service == nil {
				writeAPIError(w, http.StatusServiceUnavailable, "system_settings_unavailable", "system settings are unavailable")
				return
			}
			updated, err := service.Put(r.Context(), request)
			if errors.Is(err, systemsettings.ErrInvalidSettings) {
				writeAPIError(w, http.StatusBadRequest, "invalid_settings", "system settings are invalid")
				return
			}
			if err != nil {
				writeAPIError(w, http.StatusServiceUnavailable, "system_settings_unavailable", "system settings are unavailable")
				return
			}
			writeJSON(w, http.StatusOK, updated)
		},
	}))
}
