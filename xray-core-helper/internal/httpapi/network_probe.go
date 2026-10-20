package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/networkprobe"
	"home-router/xray-core-helper/internal/safeapply"
)

type NetworkProbeService interface {
	Probe(context.Context, api.NetworkProbeRequest) (api.NetworkProbeResult, error)
	History() api.NetworkProbeHistory
}

func registerNetworkProbe(mux *http.ServeMux, service NetworkProbeService, apply ApplyTemplateService) {
	mux.HandleFunc("/v1/network/probe", methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			if service == nil {
				writeAPIError(w, 503, "network_probe_unavailable", "network probe unavailable")
				return
			}
			writeJSON(w, 200, service.History())
		},
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			var request api.NetworkProbeRequest
			if !decodeUniqueBodyInto(w, r, &request) {
				return
			}
			if (request.Stage != "before" && request.Stage != "after" && request.Stage != "manual") || len(request.TransactionID) > 64 || (request.Stage != "after" && request.TransactionID != "") {
				writeAPIError(w, 400, "invalid_request", "invalid probe stage or transaction")
				return
			}
			if service == nil {
				writeAPIError(w, 503, "network_probe_unavailable", "network probe unavailable")
				return
			}
			ctx := r.Context()
			if request.Stage != "manual" {
				if apply == nil {
					writeAPIError(w, 503, "safe_apply_unavailable", "safe apply unavailable")
					return
				}
				pending, err := apply.Pending(r.Context())
				if err != nil {
					writeAPIError(w, 503, "safe_apply_unavailable", "safe apply unavailable")
					return
				}
				if (request.Stage == "before" && pending != nil) || (request.Stage == "after" && (pending == nil || pending.ID != request.TransactionID || pending.Phase != safeapply.PhaseApplied)) {
					writeAPIError(w, 409, "probe_transaction_conflict", "probe does not match pending transaction")
					return
				}
				if request.Stage == "after" {
					deadline := pending.Deadline.Add(-3 * time.Second)
					if !deadline.After(time.Now()) {
						writeAPIError(w, 409, "probe_deadline_expired", "not enough time before rollback; resolve pending transaction")
						return
					}
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, deadline)
					defer cancel()
				}
			}
			result, err := service.Probe(ctx, request)
			if errors.Is(err, networkprobe.ErrBusy) {
				writeAPIError(w, 409, "network_probe_busy", "network probe already running")
				return
			}
			if err != nil {
				writeAPIError(w, 503, "network_probe_unavailable", "network probe unavailable; inspect saved evidence")
				return
			}
			writeJSON(w, 200, result)
		},
	}))
}
