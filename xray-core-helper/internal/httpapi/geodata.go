package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"home-router/xray-core-helper/internal/geodatalimits"
	"io"
	"net/http"

	"home-router/xray-core-helper/internal/api"
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
		request, ok := decodeUniqueBody[api.ReplaceGeodataSettingsRequest](w, r)
		if !ok {
			return
		}
		if request.ExpectedGeneration == nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "expectedGeneration is required")
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
					writeJSON(w, executionError.HTTPStatus, api.ErrorResponse{Error: api.ErrorDetail{Code: executionError.Code, Message: (&geodatalimits.Exceeded{LimitBytes: geodatalimits.Bytes(executionError.MaxAssetSizeMB)}).Error(), MaxAssetSizeMB: executionError.MaxAssetSizeMB}})
				} else {
					writeAPIError(w, executionError.HTTPStatus, executionError.Code, "geodata update could not be completed")
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

func decodeUniqueBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var value T
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return value, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || validateUniqueJSON(trimmed) != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body must be one valid JSON object")
		return value, false
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return value, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body must contain one JSON object")
		return value, false
	}
	return value, true
}

func validateUniqueJSON(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := uniqueJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}

func uniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case nil:
		return errors.New("null values are not allowed")
	case json.Delim:
		switch value {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return errors.New("duplicate object key")
				}
				seen[key] = struct{}{}
				if err := uniqueJSONValue(decoder); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("object is incomplete")
			}
		case '[':
			for decoder.More() {
				if err := uniqueJSONValue(decoder); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("array is incomplete")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	return nil
}
