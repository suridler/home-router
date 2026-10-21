package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/fault"
	"home-router/xray-core-helper/internal/logreader"
	"home-router/xray-core-helper/internal/routerdiagnostics"
	"home-router/xray-core-helper/internal/safeapply"
	"home-router/xray-core-helper/internal/servicecontrol"
	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/xrayconfig"
	"home-router/xray-core-helper/internal/xrayvalidation"
)

const MaxRequestBodyBytes = 1 << 20

const longOperationWriteTimeout = 20 * time.Minute

type ServerServices struct {
	DomainReconcile DomainReconcileService
	NetworkProbe    NetworkProbeService
	Web             *api.WebSettingsResponse
	Admin           GeodataAdminService
	Executor        GeodataUpdateExecutor
	Validation      ValidationService
	Diagnostics     DiagnosticsService
	Connectivity    interface {
		Probe(context.Context, string) (routerdiagnostics.ConnectivityResult, error)
	}
	Services      ServiceController
	Logs          LogReadService
	Errors        ErrorHistoryService
	Logging       LoggingMutationService
	Readiness     ReadinessService
	Settings      SystemSettingsService
	Backups       TemplateBackupService
	AcceptSystem  AcceptSystemService
	ApplyTemplate ApplyTemplateService
	Templates     TemplateStatusService
	Setup         SetupService
}

type TemplateStatusService interface {
	Current() (api.TemplateStatusResponse, bool)
	Refresh(context.Context) api.TemplateStatusResponse
}

type ReadinessService interface {
	Check(context.Context) api.ReadinessResponse
}

type ValidationService interface {
	Validate(context.Context) (xrayvalidation.Result, error)
}

type DiagnosticsService interface {
	Diagnose(context.Context) (routerdiagnostics.Result, error)
}

type ServiceController interface {
	Execute(context.Context, servicecontrol.Unit, servicecontrol.Action) (servicecontrol.Result, error)
}

type LogReadService interface {
	Read(context.Context, logreader.Kind, int) (logreader.Result, error)
}

type LoggingMutationService interface {
	Update(context.Context, api.LogConfigurationMutationRequest) (transaction.Metadata, error)
}

func NewServer(version string, reader xrayconfig.Reader, adopter ConfigService, domains ProxyDomainService, dependencies ...ServerServices) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, &fault.Error{
				Code:       fault.CodeMethodNotAllowed,
				Message:    "method not allowed",
				HTTPStatus: http.StatusMethodNotAllowed,
			})
			return
		}
		writeJSON(w, http.StatusOK, api.HealthResponse{Status: "ok", Version: version})
	})
	mux.HandleFunc("/v1/ready", methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			if len(dependencies) == 0 || dependencies[0].Readiness == nil {
				writeJSON(w, http.StatusServiceUnavailable, api.ReadinessResponse{
					Status: "degraded",
					Checks: []api.ReadinessCheck{{Name: "service", Status: "degraded", Code: "readiness_unavailable"}},
				})
				return
			}
			result := dependencies[0].Readiness.Check(r.Context())
			if apply := dependencies[0].ApplyTemplate; apply != nil {
				pending, err := apply.Pending(r.Context())
				if err != nil {
					result.Status = "degraded"
					result.Checks = append(result.Checks, api.ReadinessCheck{Name: "safe_apply", Status: "degraded", Code: "safe_apply_unavailable"})
				} else if pending != nil && pending.Phase == safeapply.PhaseAwaitingReboot {
					result.Status = "degraded"
					result.Checks = append(result.Checks, api.ReadinessCheck{Name: "safe_apply", Status: "degraded", Code: "reboot_required"})
				} else if pending != nil && (pending.Phase == safeapply.PhaseRollingBack || pending.Phase == safeapply.PhasePrepared || pending.Phase == safeapply.PhaseConfirming) {
					result.Status = "degraded"
					result.Checks = append(result.Checks, api.ReadinessCheck{Name: "safe_apply", Status: "degraded", Code: "recovery_required"})
				}
			}
			status := http.StatusOK
			if result.Status != "ready" {
				status = http.StatusServiceUnavailable
			}
			writeJSON(w, status, result)
		},
	}))
	mux.HandleFunc("/v1/xray/overview", getProjection(func(ctx context.Context) (api.XrayOverviewResponse, error) { return reader.Overview(ctx), nil }))
	mux.HandleFunc("/v1/xray/runtime", getProjection(func(context.Context) (api.RuntimeResponse, error) { return reader.Runtime(), nil }))
	mux.HandleFunc("/v1/xray/exporter-metadata", getProjection(reader.ExporterMetadata))
	mux.HandleFunc("/v1/xray/status", getProjection(func(ctx context.Context) (api.StatusResponse, error) { return reader.Status(ctx), nil }))
	mux.HandleFunc("/v1/xray/config/summary", getProjection(reader.Summary))
	mux.HandleFunc("/v1/xray/inbounds", getProjection(reader.Inbounds))
	mux.HandleFunc("/v1/xray/outbounds", getProjection(reader.Outbounds))
	mux.HandleFunc("/v1/xray/routing", getProjection(reader.Routing))
	mux.HandleFunc("/v1/xray/geodata", getProjection(reader.Geodata))
	mux.HandleFunc("/v1/xray/config/adopt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, &fault.Error{Code: fault.CodeMethodNotAllowed, Message: "method not allowed", HTTPStatus: http.StatusMethodNotAllowed})
			return
		}
		if adopter == nil {
			writeJSON(w, http.StatusServiceUnavailable, api.ErrorResponse{Error: api.ErrorDetail{Code: "transaction_unavailable", Message: "configuration transaction service is unavailable"}})
			return
		}
		request, ok := decodeBody[api.AdoptConfigRequest](w, r)
		if !ok {
			return
		}
		if request.ExpectedGeneration == nil {
			writeAPIError(w, 400, "invalid_request", "expectedGeneration is required")
			return
		}

		metadata, adopted, err := adopter.Adopt(r.Context(), *request.ExpectedGeneration)
		if err != nil {
			if errors.Is(err, coordinator.ErrRecoveryBlocked) {
				writeAPIError(w, http.StatusServiceUnavailable, "transaction_unavailable", "configuration transaction service is unavailable")
				return
			}
			code, message, status := transaction.ErrorInfo(err)
			writeJSON(w, status, api.ErrorResponse{Error: api.ErrorDetail{Code: code, Message: message}})
			return
		}
		writeJSON(w, http.StatusOK, api.AdoptConfigResponse{
			Generation:  metadata.Generation,
			ContentHash: metadata.ContentHash,
			Adopted:     adopted,
		})
	})
	registerMutations(mux, adopter)
	registerProxyDomains(mux, domains)
	var services ServerServices
	if len(dependencies) > 0 {
		services = dependencies[0]
	}
	registerDomainReconcile(mux, services.DomainReconcile)
	registerGeodataAdmin(mux, services.Admin, services.Executor)
	registerOperations(mux, reader.Logging, services.Validation, services.Diagnostics, services.Services, services.Logs, services.Logging)
	registerErrorHistory(mux, services.Errors)
	registerConnectivity(mux, services)
	registerNetworkProbe(mux, services.NetworkProbe, services.ApplyTemplate)
	registerWebSettings(mux, services.Web)
	if services.Setup != nil {
		setupHandler := NewSetupServer(version, services.Setup).Handler
		for _, path := range []string{"/v1/setup", "/v1/setup/interfaces", "/v1/setup/plan", "/v1/setup/apply", "/v1/setup/adopt", "/v1/setup/recover"} {
			mux.Handle(path, setupHandler)
		}
	}
	registerTemplateStatus(mux, services.Templates)
	registerSystemSettings(mux, services.Settings)
	registerTemplateBackups(mux, services.Backups, services.Templates, services.AcceptSystem, services.ApplyTemplate)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, &fault.Error{
			Code:       fault.CodeNotFound,
			Message:    "endpoint not found",
			HTTPStatus: http.StatusNotFound,
		})
	})

	return &http.Server{
		Handler:           limitRequestBody(rebootGate(mux, services.ApplyTemplate)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      longOperationWriteTimeout,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

func registerTemplateStatus(mux *http.ServeMux, service TemplateStatusService) {
	mux.HandleFunc("/v1/templates", methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, _ *http.Request) {
			if service == nil {
				writeAPIError(w, http.StatusServiceUnavailable, "template_status_unavailable", "template status is unavailable")
				return
			}
			result, ok := service.Current()
			if !ok {
				writeAPIError(w, http.StatusServiceUnavailable, "template_status_unavailable", "template status is unavailable")
				return
			}
			writeJSON(w, http.StatusOK, result)
		},
	}))
	mux.HandleFunc("/v1/templates/refresh", methods(map[string]http.HandlerFunc{
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			if service == nil {
				writeAPIError(w, http.StatusServiceUnavailable, "template_status_unavailable", "template status is unavailable")
				return
			}
			if r.ContentLength > 0 {
				writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body must be empty")
				return
			}
			writeJSON(w, http.StatusOK, service.Refresh(r.Context()))
		},
	}))
}

func getProjection[T any](project func(context.Context) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, &fault.Error{
				Code:       fault.CodeMethodNotAllowed,
				Message:    "method not allowed",
				HTTPStatus: http.StatusMethodNotAllowed,
			})
			return
		}
		payload, err := project(r.Context())
		if err != nil {
			code, message, status := xrayconfig.ErrorInfo(err)
			writeJSON(w, status, api.ErrorResponse{Error: api.ErrorDetail{Code: code, Message: message}})
			return
		}
		writeJSON(w, http.StatusOK, payload)
	}
}

func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > MaxRequestBodyBytes {
			writeError(w, &fault.Error{
				Code:       fault.CodeRequestTooLarge,
				Message:    "request body exceeds the size limit",
				HTTPStatus: http.StatusRequestEntityTooLarge,
			})
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes))
		if err != nil {
			writeError(w, &fault.Error{
				Code:       fault.CodeRequestTooLarge,
				Message:    "request body exceeds the size limit",
				HTTPStatus: http.StatusRequestEntityTooLarge,
			})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, err *fault.Error) {
	writeJSON(w, err.HTTPStatus, api.ErrorResponse{Error: api.ErrorDetail{Code: string(err.Code), Message: err.Message}})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

type ProxyDomainService interface {
	Snapshot(context.Context) (domainstore.Snapshot, error)
	Add(context.Context, string) error
	Delete(context.Context, string) error
	Apply(context.Context) error
}
