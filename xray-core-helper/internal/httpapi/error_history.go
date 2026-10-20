package httpapi

import (
	"context"
	"home-router/xray-core-helper/internal/api"
	"net/http"
	"strconv"
)

type ErrorHistoryService interface {
	Collect(context.Context) error
	List(context.Context, int) (api.ErrorHistoryResponse, error)
}

func registerErrorHistory(mux *http.ServeMux, service ErrorHistoryService) {
	unavailable := func(w http.ResponseWriter) {
		writeAPIError(w, 503, "error_history_unavailable", "error history is unavailable")
	}
	mux.HandleFunc("/v1/errors", methods(map[string]http.HandlerFunc{http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		query := r.URL.Query()
		if len(query) > 1 || (len(query) == 1 && !query.Has("limit")) || len(query["limit"]) > 1 {
			writeAPIError(w, 400, "invalid_limit", "limit must be between 1 and 100")
			return
		}
		if query.Has("limit") {
			n, err := strconv.Atoi(query.Get("limit"))
			if err != nil || n < 1 || n > 100 {
				writeAPIError(w, 400, "invalid_limit", "limit must be between 1 and 100")
				return
			}
			limit = n
		}
		if service == nil {
			unavailable(w)
			return
		}
		result, err := service.List(r.Context(), limit)
		if err != nil {
			unavailable(w)
			return
		}
		writeJSON(w, 200, result)
	}}))
	mux.HandleFunc("/v1/errors/collect", methods(map[string]http.HandlerFunc{http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			writeAPIError(w, 400, "invalid_request", "query parameters are not accepted")
			return
		}
		if !emptyObjectBody(w, r) {
			return
		}
		if service == nil {
			unavailable(w)
			return
		}
		if service.Collect(r.Context()) != nil {
			unavailable(w)
			return
		}
		result, err := service.List(r.Context(), 5)
		if err != nil {
			unavailable(w)
			return
		}
		writeJSON(w, 200, result)
	}}))
}
