package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/geodatalimits"
)

type GeodataAdminService interface {
	GetSettings(context.Context) (api.VersionedGeodataSettings, error)
	GetStatus(context.Context) (api.GeodataStatusResponse, error)
	PutSettings(context.Context, uint64, api.GeodataSettings) (api.VersionedGeodataSettings, error)
}

type GeodataUpdateExecutor interface {
	Execute(context.Context, string) (api.GeodataOperation, error)
}

func registerGeodataAdmin(mux *http.ServeMux, service GeodataAdminService, executor GeodataUpdateExecutor) {
	mux.HandleFunc("/v1/xray/geodata/settings", methods(map[string]http.HandlerFunc{
		http.MethodGet: getGeodataSettings(service),
		http.MethodPut: putGeodataSettings(service),
	}))
	mux.HandleFunc("/v1/xray/geodata/update/status", methods(map[string]http.HandlerFunc{
		http.MethodGet: getGeodataStatus(service),
	}))
	mux.HandleFunc("/v1/xray/geodata/update", methods(map[string]http.HandlerFunc{
		http.MethodPost: postGeodataUpdate(executor),
	}))
}

func getGeodataSettings(service GeodataAdminService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "geodata_settings_unavailable", "geodata settings are unavailable")
			return
		}
		settings, err := service.GetSettings(r.Context())
		if err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "geodata_settings_unavailable", "geodata settings are unavailable")
			return
		}
		writeJSON(w, http.StatusOK, settings)
	}
}

func putGeodataSettings(service GeodataAdminService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		request, ok := decodeBody[api.ReplaceGeodataSettingsRequest](w, r)
		if !ok {
			return
		}
		if request.ExpectedGeneration == nil || request.Settings.Origins == nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "expectedGeneration and settings.origins are required")
			return
		}
		if service == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "geodata_settings_unavailable", "geodata settings are unavailable")
			return
		}
		updated, err := service.PutSettings(r.Context(), *request.ExpectedGeneration, request.Settings)
		if err != nil {
			var contractError *api.ContractError
			if errors.As(err, &contractError) {
				writeAPIError(w, contractError.HTTPStatus, contractError.Code, "geodata settings could not be updated")
				return
			}
			writeAPIError(w, http.StatusServiceUnavailable, "geodata_settings_unavailable", "geodata settings are unavailable")
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

func getGeodataStatus(service GeodataAdminService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "geodata_status_unavailable", "geodata update status is unavailable")
			return
		}
		status, err := service.GetStatus(r.Context())
		if err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "geodata_status_unavailable", "geodata update status is unavailable")
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func postGeodataUpdate(executor GeodataUpdateExecutor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			body, err := io.ReadAll(r.Body)
			if err != nil || len(bytes.TrimSpace(body)) != 0 {
				writeAPIError(w, http.StatusBadRequest, "invalid_request", "update request body must be empty")
				return
			}
		}
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || !api.ValidIdempotencyKey(keys[0]) {
			writeAPIError(w, http.StatusBadRequest, "invalid_idempotency_key", "one bounded Idempotency-Key header is required")
			return
		}
		if executor == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "geodata_update_unavailable", "geodata update execution is unavailable")
			return
		}
		operation, err := executor.Execute(r.Context(), keys[0])
		if err != nil {
			var executionError *api.ContractError
			if errors.As(err, &executionError) {
				if executionError.Code == "geodata_asset_too_large" && geodatalimits.ValidMB(executionError.MaxAssetSizeMB) {
					message := executionError.Message
					if message == "" {
						message = (&geodatalimits.Exceeded{LimitBytes: geodatalimits.Bytes(executionError.MaxAssetSizeMB)}).Error()
					}
					writeJSON(w, executionError.HTTPStatus, struct {
						Error     api.ErrorDetail      `json:"error"`
						Operation api.GeodataOperation `json:"operation"`
					}{api.ErrorDetail{Code: executionError.Code, Message: message, MaxAssetSizeMB: executionError.MaxAssetSizeMB}, operation})
				} else {
					message := executionError.Message
					if message == "" {
						message = "Обновление не выполнено; подробности: /v1/xray/geodata/update/status"
					}
					writeJSON(w, executionError.HTTPStatus, struct {
						Error     api.ErrorDetail      `json:"error"`
						Operation api.GeodataOperation `json:"operation"`
					}{api.ErrorDetail{Code: executionError.Code, Message: message}, operation})
				}
			} else {
				writeAPIError(w, http.StatusServiceUnavailable, "geodata_operation_unavailable", "geodata update could not be completed")
			}
			return
		}
		status := http.StatusOK
		if operation.State == "running" {
			status = http.StatusAccepted
		}
		writeJSON(w, status, api.GeodataUpdateResponse{Operation: operation})
	}
}
