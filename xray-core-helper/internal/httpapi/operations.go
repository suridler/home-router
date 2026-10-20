package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/logreader"
	"home-router/xray-core-helper/internal/servicecontrol"
	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/xrayvalidation"
)

const defaultLogLineLimit = 100

func registerOperations(
	mux *http.ServeMux,
	loggingProjection func(context.Context) (api.LoggingResponse, error),
	validation ValidationService,
	diagnostics DiagnosticsService,
	services ServiceController,
	logs LogReadService,
	logging LoggingMutationService,
) {
	mux.HandleFunc("/v1/xray/config/validate", methods(map[string]http.HandlerFunc{
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			if !emptyObjectBody(w, r) {
				return
			}
			if validation == nil {
				writeAPIError(w, http.StatusServiceUnavailable, "validation_unavailable", "Xray validation is unavailable")
				return
			}
			result, err := validation.Validate(r.Context())
			if err != nil {
				writeValidationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, api.ValidationResponse{Status: string(result.Status)})
		},
	}))

	mux.HandleFunc("/v1/xray/diagnostics", methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			if diagnostics == nil {
				writeAPIError(w, http.StatusServiceUnavailable, "diagnostics_unavailable", "router diagnostics are unavailable")
				return
			}
			result, err := diagnostics.Diagnose(r.Context())
			if err != nil {
				writeAPIError(w, http.StatusServiceUnavailable, "diagnostics_unavailable", "router diagnostics are unavailable")
				return
			}
			checks := make([]api.DiagnosticCheck, 0, len(result.Checks))
			for _, check := range result.Checks {
				checks = append(checks, api.DiagnosticCheck{Name: string(check.Name), Status: string(check.Status), Code: check.Code, TemplateID: check.TemplateID, BindingID: check.BindingID})
			}
			writeJSON(w, http.StatusOK, api.DiagnosticsResponse{Checks: checks})
		},
	}))

	mux.HandleFunc("/v1/xray/services/{unit}/{action}", methods(map[string]http.HandlerFunc{
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			if !emptyObjectBody(w, r) {
				return
			}
			unit, unitOK := serviceUnit(r.PathValue("unit"))
			action, actionOK := serviceAction(r.PathValue("action"))
			if !unitOK || !actionOK {
				writeAPIError(w, http.StatusBadRequest, "invalid_service_action", "service unit or action is not allowed")
				return
			}
			if unit == servicecontrol.UnitTProxy && (action == servicecontrol.ActionStart || action == servicecontrol.ActionRestart) {
				writeAPIError(w, 409, "safe_apply_required", "activate the tproxy-unit template with confirmed safe apply")
				return
			}
			if services == nil {
				writeAPIError(w, http.StatusServiceUnavailable, "service_control_unavailable", "service control is unavailable")
				return
			}
			result, err := services.Execute(r.Context(), unit, action)
			if err != nil {
				writeServiceError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, api.ServiceActionResponse{
				Unit: string(result.Unit), Action: string(result.Action), State: string(result.State),
			})
		},
	}))

	mux.HandleFunc("/v1/logs/{kind}", methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			kind, ok := logKind(r.PathValue("kind"))
			if !ok {
				writeAPIError(w, http.StatusBadRequest, "invalid_kind", "log kind must be access or error")
				return
			}
			limit, ok := logLimit(r)
			if !ok {
				writeAPIError(w, http.StatusBadRequest, "invalid_limit", "log limit must be an integer between 1 and 200")
				return
			}
			if logs == nil {
				writeAPIError(w, http.StatusServiceUnavailable, "log_unavailable", "log reader is unavailable")
				return
			}
			result, err := logs.Read(r.Context(), kind, limit)
			if err != nil {
				writeLogError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, api.LogReadResponse{
				Kind: string(result.Kind), Source: string(result.Source), Lines: result.Lines,
				BytesRead: result.BytesRead, ByteLimit: result.ByteLimit,
				LineLimit: result.LineLimit, Truncated: result.Truncated,
			})
		},
	}))

	mux.HandleFunc("/v1/xray/logging", methods(map[string]http.HandlerFunc{
		http.MethodGet: getProjection(loggingProjection),
		http.MethodPut: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.LogConfigurationMutationRequest](w, r)
			if !ok {
				return
			}
			if logging == nil {
				writeAPIError(w, http.StatusServiceUnavailable, "logging_unavailable", "logging mutation is unavailable")
				return
			}
			metadata, err := logging.Update(r.Context(), request)
			if err != nil {
				code, message, status := transaction.ErrorInfo(err)
				writeAPIError(w, status, code, message)
				return
			}
			writeJSON(w, http.StatusOK, api.MutationResponse{Generation: metadata.Generation, ContentHash: metadata.ContentHash})
		},
	}))
}

func emptyObjectBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return true
	}
	writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body must be empty or contain one empty JSON object")
	return false
}

func serviceUnit(value string) (servicecontrol.Unit, bool) {
	switch value {
	case "xray":
		return servicecontrol.UnitXray, true
	case "tproxy":
		return servicecontrol.UnitTProxy, true
	default:
		return "", false
	}
}

func serviceAction(value string) (servicecontrol.Action, bool) {
	switch servicecontrol.Action(value) {
	case servicecontrol.ActionStart, servicecontrol.ActionStop, servicecontrol.ActionRestart:
		return servicecontrol.Action(value), true
	default:
		return "", false
	}
}

func logKind(value string) (logreader.Kind, bool) {
	switch value {
	case string(logreader.Access):
		return logreader.Access, true
	case string(logreader.ErrorLog):
		return logreader.ErrorLog, true
	default:
		return "", false
	}
}

func logLimit(r *http.Request) (int, bool) {
	query := r.URL.Query()
	if len(query) == 0 {
		return defaultLogLineLimit, true
	}
	values, ok := query["limit"]
	if !ok || len(query) != 1 || len(values) != 1 || values[0] == "" {
		return 0, false
	}
	limit, err := strconv.Atoi(values[0])
	return limit, err == nil && limit >= 1 && limit <= logreader.MaxLines
}

func writeValidationError(w http.ResponseWriter, err error) {
	var failure *xrayvalidation.Error
	if !errors.As(err, &failure) {
		writeAPIError(w, http.StatusServiceUnavailable, "validation_unavailable", "Xray validation is unavailable")
		return
	}
	status := http.StatusServiceUnavailable
	if failure.Code == xrayvalidation.CodeValidationFailed {
		status = http.StatusUnprocessableEntity
	}
	writeAPIError(w, status, string(failure.Code), "Xray validation failed")
}

func writeServiceError(w http.ResponseWriter, err error) {
	var failure *servicecontrol.Error
	code := "service_control_failed"
	if errors.As(err, &failure) {
		code = string(failure.Code)
	}
	writeAPIError(w, http.StatusServiceUnavailable, code, "service action failed")
}

func writeLogError(w http.ResponseWriter, err error) {
	var failure *logreader.Error
	if !errors.As(err, &failure) {
		writeAPIError(w, http.StatusServiceUnavailable, "log_unavailable", "log read failed")
		return
	}
	status := http.StatusServiceUnavailable
	if failure.Code == logreader.ErrorInvalidKind || failure.Code == logreader.ErrorInvalidLimit || failure.Code == logreader.ErrorInvalidPath {
		status = http.StatusBadRequest
	}
	writeAPIError(w, status, string(failure.Code), "log read failed")
}
