package httpapi

import (
	"context"
	"net/http"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/transaction"
)

type ConfigRollbackService interface {
	RollbackStatus(context.Context) (transaction.RollbackStatus, error)
	Rollback(context.Context, uint64) (transaction.Metadata, error)
}

func registerConfigRollback(mux *http.ServeMux, service ConfigRollbackService) {
	mux.HandleFunc("/v1/xray/config/rollback", methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			if service == nil {
				writeAPIError(w, 503, "config_rollback_unavailable", "Configuration rollback is unavailable")
				return
			}
			status, err := service.RollbackStatus(r.Context())
			if err != nil {
				code, message, status := transaction.ErrorInfo(err)
				writeAPIError(w, status, code, message)
				return
			}
			writeJSON(w, 200, status)
		},
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			if service == nil {
				writeAPIError(w, 503, "config_rollback_unavailable", "Configuration rollback is unavailable")
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
			metadata, err := service.Rollback(r.Context(), *request.ExpectedGeneration)
			if err != nil {
				code, message, status := transaction.ErrorInfo(err)
				writeAPIError(w, status, code, message)
				return
			}
			writeJSON(w, 200, api.MutationResponse{Generation: metadata.Generation, ContentHash: metadata.ContentHash})
		},
	}))
}
