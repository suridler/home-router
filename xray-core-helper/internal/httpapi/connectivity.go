package httpapi

import (
	"home-router/xray-core-helper/internal/routerdiagnostics"
	"net/http"
	"time"
)

func registerConnectivity(mux *http.ServeMux, services ServerServices) {
	mux.HandleFunc("/v1/xray/diagnostics/connectivity", methods(map[string]http.HandlerFunc{http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			OutboundTag string `json:"outboundTag"`
		}
		if !decodeUniqueBodyInto(w, r, &request) {
			return
		}
		if services.Connectivity == nil {
			writeAPIError(w, 503, "connectivity_unavailable", "connectivity diagnostics unavailable")
			return
		}
		result, err := services.Connectivity.Probe(r.Context(), request.OutboundTag)
		if err != nil {
			writeAPIError(w, 503, "connectivity_unavailable", "connectivity diagnostics unavailable")
			return
		}
		var evidence any
		if services.Diagnostics != nil {
			diagnostics, err := services.Diagnostics.Diagnose(r.Context())
			if err == nil {
				checks := make([]map[string]string, 0, len(diagnostics.Checks))
				for _, check := range diagnostics.Checks {
					checks = append(checks, map[string]string{"name": string(check.Name), "status": string(check.Status)})
				}
				evidence = checks
				for _, check := range diagnostics.Checks {
					if check.Name != routerdiagnostics.CheckXrayService && check.Name != routerdiagnostics.CheckTProxyService {
						continue
					}
					result.Layers = append(result.Layers, routerdiagnostics.Layer{Name: string(check.Name), Status: string(check.Status), Code: string(check.Name) + "_" + string(check.Status), Scope: "local_service_state", CheckedAt: time.Now().UTC(), TimeoutMS: 10000})
				}
			}
		}
		writeJSON(w, 200, map[string]any{"layers": result.Layers, "routingEvidence": evidence})
	}}))
}
