package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/fault"
	"home-router/xray-core-helper/internal/jsonrequest"
	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/vless"
	"home-router/xray-core-helper/internal/xrayconfig"
)

// ConfigService is the single writer behind every configuration mutation.
type ConfigService interface {
	Adopt(context.Context, uint64) (transaction.Metadata, bool, error)
	Mutate(context.Context, uint64, transaction.Planner, bool) (transaction.Metadata, error)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, api.ErrorResponse{Error: api.ErrorDetail{Code: code, Message: message}})
}

func decodeBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var value T
	ok := decodeBodyInto(w, r, &value)
	return value, ok
}

func decodeBodyInto(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := jsonrequest.Decode(r.Body, target); err != nil {
		writeAPIError(w, 400, "invalid_request", "request body must contain one valid JSON object without unknown or repeated fields")
		return false
	}
	return true
}

func methods(handlers map[string]http.HandlerFunc) http.HandlerFunc {
	allowed := make([]string, 0, len(handlers))
	for method := range handlers {
		allowed = append(allowed, method)
	}
	sort.Strings(allowed)
	return func(w http.ResponseWriter, r *http.Request) {
		if handler, ok := handlers[r.Method]; ok {
			handler(w, r)
			return
		}
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		writeError(w, &fault.Error{Code: fault.CodeMethodNotAllowed, Message: "method not allowed", HTTPStatus: http.StatusMethodNotAllowed})
	}
}

func runMutation(w http.ResponseWriter, r *http.Request, service ConfigService, request api.MutationRequest, plan transaction.Planner) {
	if request.ExpectedGeneration == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "expectedGeneration is required")
		return
	}
	if service == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "transaction_unavailable", "configuration transaction service is unavailable")
		return
	}
	metadata, err := service.Mutate(r.Context(), *request.ExpectedGeneration, plan, request.Restart)
	if err != nil {
		if errors.Is(err, coordinator.ErrRecoveryBlocked) {
			writeAPIError(w, http.StatusServiceUnavailable, "transaction_unavailable", "configuration transaction service is unavailable")
			return
		}
		code, message, status := transaction.ErrorInfo(err)
		writeAPIError(w, status, code, message)
		return
	}
	writeJSON(w, http.StatusOK, api.MutationResponse{Generation: metadata.Generation, ContentHash: metadata.ContentHash})
}

func registerMutations(mux *http.ServeMux, service ConfigService) {
	mux.HandleFunc("/v1/xray/outbounds/vless/preview", methods(map[string]http.HandlerFunc{
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.VlessPreviewRequest](w, r)
			if !ok {
				return
			}
			parsed, err := vless.Parse(request.URI, vless.TransportKeyNetwork)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_vless_uri", err.Error())
				return
			}
			preview := parsed.Preview
			writeJSON(w, http.StatusOK, api.VlessPreviewResponse{
				Address: preview.Address, Port: preview.Port, Transport: preview.Transport,
				Security: preview.Security, Flow: preview.Flow, ServerName: preview.ServerName,
				UnsupportedParameters: preview.UnsupportedParameters,
			})
		},
	}))
	mux.HandleFunc("/v1/xray/outbounds/vless", methods(map[string]http.HandlerFunc{
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.AddVlessOutboundRequest](w, r)
			if !ok {
				return
			}
			runMutation(w, r, service, request.MutationRequest, xrayconfig.PlanAddVlessOutbound(request.Tag, request.URI))
		},
	}))
	mux.HandleFunc("/v1/xray/outbounds/{tag}/vless", methods(map[string]http.HandlerFunc{
		http.MethodPut: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.UpdateVlessOutboundRequest](w, r)
			if !ok {
				return
			}
			runMutation(w, r, service, request.MutationRequest, xrayconfig.PlanUpdateVlessOutbound(r.PathValue("tag"), request.URI))
		},
	}))
	mux.HandleFunc("/v1/xray/outbounds/{tag}", methods(map[string]http.HandlerFunc{
		http.MethodDelete: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.MutationRequest](w, r)
			if !ok {
				return
			}
			runMutation(w, r, service, request, xrayconfig.PlanDeleteOutbound(r.PathValue("tag")))
		},
	}))
	mux.HandleFunc("/v1/xray/routing/rules", methods(map[string]http.HandlerFunc{
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.AddRuleRequest](w, r)
			if !ok {
				return
			}
			runMutation(w, r, service, request.MutationRequest, xrayconfig.PlanAddRule(request.Rule, request.Position))
		},
	}))
	mux.HandleFunc("/v1/xray/routing/rules/clone", methods(map[string]http.HandlerFunc{
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.CloneRuleRequest](w, r)
			if !ok {
				return
			}
			runMutation(w, r, service, request.MutationRequest, xrayconfig.PlanCloneRule(request.Position, request.RuleTag))
		},
	}))
	mux.HandleFunc("/v1/xray/routing/rules/{id}/move", methods(map[string]http.HandlerFunc{
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.MoveRuleRequest](w, r)
			if !ok {
				return
			}
			runMutation(w, r, service, request.MutationRequest, xrayconfig.PlanMoveRule(r.PathValue("id"), request.Direction))
		},
	}))
	mux.HandleFunc("/v1/xray/routing/rules/{id}", methods(map[string]http.HandlerFunc{
		http.MethodPut: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.EditRuleRequest](w, r)
			if !ok {
				return
			}
			runMutation(w, r, service, request.MutationRequest, xrayconfig.PlanEditRule(r.PathValue("id"), request.Rule))
		},
		http.MethodDelete: func(w http.ResponseWriter, r *http.Request) {
			request, ok := decodeBody[api.MutationRequest](w, r)
			if !ok {
				return
			}
			runMutation(w, r, service, request, xrayconfig.PlanDeleteRule(r.PathValue("id")))
		},
	}))
}
