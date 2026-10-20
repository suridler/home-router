package httpapi

import (
	"context"
	"errors"
	"net/http"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/coordinator"
)

type DomainReconcileService interface {
	Reconcile(context.Context) (bool, error)
}

func registerDomainReconcile(mux *http.ServeMux, service DomainReconcileService) {
	mux.HandleFunc("/v1/proxy-domains/reconcile", methods(map[string]http.HandlerFunc{
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			if !emptyRequestBody(w, r) {
				return
			}
			if service == nil {
				writeProxyDomainUnavailable(w)
				return
			}
			applied, err := service.Reconcile(r.Context())
			if err != nil {
				if errors.Is(err, coordinator.ErrRecoveryBlocked) {
					writeRecoveryBlocked(w)
					return
				}
				writeAPIError(w, http.StatusServiceUnavailable, "domain_runtime_reconcile_failed", "proxy-domain runtime restoration could not complete")
				return
			}
			writeJSON(w, http.StatusOK, api.ApplyProxyDomainsResponse{Applied: applied})
		},
	}))
}
