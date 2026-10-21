package httpapi

import (
	"encoding/json"
	"net/http"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/outbound"
	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/xrayconfig"
)

func outboundReadError(w http.ResponseWriter, err error) {
	code, message, status := transaction.ErrorInfo(err)
	if code == "transaction_failed" {
		code, message, status = xrayconfig.ErrorInfo(err)
	}
	writeAPIError(w, status, code, message)
}

func registerOutboundEditor(mux *http.ServeMux, reader xrayconfig.Reader, service ConfigService) {
	mux.HandleFunc("/v1/xray/outbound-drafts/import", methods(map[string]http.HandlerFunc{
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.OutboundImportRequest](w, r)
			if !ok {
				return
			}
			raw, err := outbound.Import(request.URI)
			if err != nil {
				writeAPIError(w, 400, "invalid_connection_uri", "connection URI is unsupported or invalid")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, 200, api.OutboundImportResponse{Outbound: raw, Warnings: []string{outbound.ImportWarning}})
		},
	}))
	mux.HandleFunc("/v1/xray/outbound-drafts/apply", methods(map[string]http.HandlerFunc{
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.OutboundWriteRequest](w, r)
			if !ok {
				return
			}
			var identity struct {
				Tag string `json:"tag"`
			}
			if json.Unmarshal(request.Outbound, &identity) != nil || request.ConfirmReplacement {
				writeAPIError(w, 400, "invalid_request", "new outbound is required")
				return
			}
			runMutation(w, r, service, request.MutationRequest, xrayconfig.PlanWriteOutbound(identity.Tag, request.Outbound, "create"))
		},
	}))
	mux.HandleFunc("/v1/xray/outbounds/{tag}/references", methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			result, err := reader.OutboundReferences(r.Context(), r.PathValue("tag"))
			if err != nil {
				outboundReadError(w, err)
				return
			}
			writeJSON(w, 200, result)
		},
	}))
	mux.HandleFunc("/v1/xray/outbounds/{tag}/replace", methods(map[string]http.HandlerFunc{
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.OutboundWriteRequest](w, r)
			if !ok {
				return
			}
			if !request.ConfirmReplacement {
				writeAPIError(w, 400, "replacement_confirmation_required", "explicit replacement confirmation is required")
				return
			}
			runMutation(w, r, service, request.MutationRequest, xrayconfig.PlanWriteOutbound(r.PathValue("tag"), request.Outbound, "replace"))
		},
	}))
	mux.HandleFunc("/v1/xray/outbounds/{tag}", methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			result, err := reader.OutboundEdit(r.Context(), r.PathValue("tag"))
			if err != nil {
				outboundReadError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, 200, result)
		},
		http.MethodPut: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.OutboundWriteRequest](w, r)
			if !ok {
				return
			}
			if request.ConfirmReplacement {
				writeAPIError(w, 400, "invalid_request", "use the explicit replacement operation")
				return
			}
			runMutation(w, r, service, request.MutationRequest, xrayconfig.PlanWriteOutbound(r.PathValue("tag"), request.Outbound, "edit"))
		},
		http.MethodDelete: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.MutationRequest](w, r)
			if !ok {
				return
			}
			runMutation(w, r, service, request, xrayconfig.PlanDeleteOutbound(r.PathValue("tag")))
		},
	}))
}
