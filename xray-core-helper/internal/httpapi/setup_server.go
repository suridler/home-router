package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/setup"
)

type SetupService interface {
	Status() setup.ConfigProbe
	Interfaces(context.Context) ([]setup.Interface, error)
	Plan(context.Context, string) (setup.Plan, error)
	Apply(context.Context, setup.Plan) (setup.Result, error)
	Recover(context.Context) error
}

func NewSetupServer(version string, service SetupService, web ...api.WebSettingsResponse) *http.Server {
	mux := http.NewServeMux()
	if len(web) > 0 {
		registerWebSettings(mux, &web[0])
	} else {
		registerWebSettings(mux, nil)
	}
	mux.HandleFunc("/v1/health", methods(map[string]http.HandlerFunc{http.MethodGet: func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, api.HealthResponse{Status: setupStatus(service.Status()), Version: version})
	}}))
	mux.HandleFunc("/v1/setup", methods(map[string]http.HandlerFunc{http.MethodGet: func(w http.ResponseWriter, _ *http.Request) {
		probe := service.Status()
		response := api.SetupStatusResponse{ConfigFingerprint: probe.Fingerprint, Status: setupStatus(probe), ConfigState: string(probe.State), SetupRequired: probe.SetupRequired, RequiresChoice: probe.RequiresChoice, RecoveryRequired: probe.RecoveryRequired, MutationsBlocked: probe.RecoveryRequired}
		if op := probe.Operation; op != nil {
			response.Operation = &api.SetupOperation{NetworkInterface: op.NetworkInterface, LANNetwork: op.LANNetwork, NetworkPrepared: op.NetworkPrepared, ID: op.ID, Status: op.Status, Phase: op.Phase, ErrorCode: op.ErrorCode, UpdatedAt: op.UpdatedAt}
			if op.Status == "applying" {
				response.Status = "applying"
				response.MutationsBlocked = true
			}
		}
		writeJSON(w, http.StatusOK, response)
	}}))
	mux.HandleFunc("/v1/setup/interfaces", methods(map[string]http.HandlerFunc{http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
		items, err := service.Interfaces(r.Context())
		if err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "setup_discovery_unavailable", "network discovery is unavailable")
			return
		}
		response := api.SetupInterfacesResponse{Interfaces: make([]api.SetupInterface, 0, len(items))}
		for _, item := range items {
			response.Interfaces = append(response.Interfaces, api.SetupInterface{Name: item.Name, Addresses: item.Addresses, DefaultRoute: item.DefaultRoute, Excluded: item.Excluded, Reason: item.Reason})
		}
		writeJSON(w, http.StatusOK, response)
	}}))
	mux.HandleFunc("/v1/setup/plan", methods(map[string]http.HandlerFunc{http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
		var request api.SetupPlanRequest
		if !decodeBodyInto(w, r, &request) {
			return
		}
		var plan setup.Plan
		var err error
		if request.ReplaceXray {
			if selector, ok := service.(interface {
				PlanWithConfig(context.Context, string, string, bool) (setup.Plan, error)
			}); ok {
				plan, err = selector.PlanWithConfig(r.Context(), request.Interface, request.Address, true)
			} else {
				err = setup.ErrInvalidInterface
			}
		} else if selector, ok := service.(interface {
			PlanAddress(context.Context, string, string) (setup.Plan, error)
		}); ok {
			plan, err = selector.PlanAddress(r.Context(), request.Interface, request.Address)
		} else if request.Address != "" {
			err = setup.ErrInvalidInterface
		} else {
			plan, err = service.Plan(r.Context(), request.Interface)
		}
		if err != nil {
			writeSetupError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"plan": plan})
	}}))
	mux.HandleFunc("/v1/setup/apply", methods(map[string]http.HandlerFunc{http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
		var request setup.Plan
		if !decodeBodyInto(w, r, &request) {
			return
		}
		result, err := service.Apply(r.Context(), request)
		if err != nil {
			writeSetupError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}}))
	mux.HandleFunc("/v1/setup/adopt", methods(map[string]http.HandlerFunc{http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ConfigFingerprint string `json:"configFingerprint"`
		}
		if !decodeBodyInto(w, r, &request) {
			return
		}
		adopter, ok := service.(interface {
			Adopt(context.Context, string) (setup.Result, error)
		})
		if !ok {
			writeAPIError(w, 503, "setup_unavailable", "setup unavailable")
			return
		}
		result, err := adopter.Adopt(r.Context(), request.ConfigFingerprint)
		if err != nil {
			writeSetupError(w, err)
			return
		}
		writeJSON(w, 200, result)
	}}))
	mux.HandleFunc("/v1/setup/recover", methods(map[string]http.HandlerFunc{http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > 0 {
			writeAPIError(w, 400, "invalid_request", "request body must be empty")
			return
		}
		if err := service.Recover(r.Context()); err != nil {
			writeSetupError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"recovered": true})
	}}))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found")
	})
	return &http.Server{Handler: limitRequestBody(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 6 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
}

func writeSetupError(w http.ResponseWriter, err error) {
	var preflight *setup.PreflightError
	if errors.As(err, &preflight) {
		writeAPIError(w, 422, preflight.Code, "setup preflight failed; check binaries, Xray API capabilities, identities, assets and listener ports")
		return
	}
	switch {
	case errors.Is(err, setup.ErrSetupNotRequired):
		writeAPIError(w, 409, "setup_not_required", "setup is already complete")
	case errors.Is(err, setup.ErrRecoveryRequired):
		writeAPIError(w, 503, "recovery_required", "setup recovery is required")
	case errors.Is(err, setup.ErrInterfaceRequired):
		writeAPIError(w, 409, "interface_required", "network interface selection is required")
	case errors.Is(err, setup.ErrAddressRequired):
		writeAPIError(w, 409, "address_required", "interface has multiple IPv4 addresses; select one explicitly")
	case errors.Is(err, setup.ErrInvalidInterface):
		writeAPIError(w, 422, "invalid_interface", "network interface is unavailable")
	case errors.Is(err, setup.ErrSetupBusy):
		writeAPIError(w, 409, "setup_busy", "setup recovery is required")
	default:
		writeAPIError(w, 503, "setup_failed", "setup operation failed")
	}
}

func setupStatus(probe setup.ConfigProbe) string {
	if probe.RecoveryRequired {
		return "recovery_required"
	}
	if probe.SetupRequired {
		return "setup_required"
	}
	return "configured"
}
