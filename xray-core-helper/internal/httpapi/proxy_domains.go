package httpapi

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/domain"
	"home-router/xray-core-helper/internal/domainservice"
	"home-router/xray-core-helper/internal/domainstore"
)

func registerProxyDomains(mux *http.ServeMux, service ProxyDomainService) {
	mux.HandleFunc("/v1/proxy-domains", methods(map[string]http.HandlerFunc{
		http.MethodGet:  proxyDomainSnapshot(service),
		http.MethodPost: addProxyDomain(service),
	}))
	mux.HandleFunc("/v1/proxy-domains/apply", methods(map[string]http.HandlerFunc{
		http.MethodPost: applyProxyDomains(service),
	}))
	mux.HandleFunc("/v1/proxy-domains/{domain}", methods(map[string]http.HandlerFunc{
		http.MethodDelete: deleteProxyDomain(service),
	}))
	mux.HandleFunc("/v1/proxy-domains/", func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found")
	})
}

func proxyDomainSnapshot(service ProxyDomainService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			writeProxyDomainUnavailable(w)
			return
		}
		snapshot, err := service.Snapshot(r.Context())
		if err != nil {
			writeProxyDomainUnavailable(w)
			return
		}
		writeJSON(w, http.StatusOK, proxyDomainsResponse(snapshot))
	}
}

func addProxyDomain(service ProxyDomainService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		request, ok := decodeBody[api.AddProxyDomainRequest](w, r)
		if !ok {
			return
		}
		name, _, err := domain.NormalizeResource(request.Domain)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_domain", "resource must be a valid DNS name or IPv4 address; IPv6 and CIDR are unsupported")
			return
		}
		if service == nil {
			writeProxyDomainUnavailable(w)
			return
		}
		if err := service.Add(r.Context(), name); err != nil {
			if errors.Is(err, coordinator.ErrRecoveryBlocked) {
				writeRecoveryBlocked(w)
				return
			}
			writeProxyDomainMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, api.ProxyDomainMutationResponse{Domain: name, Applied: true})
	}
}

func deleteProxyDomain(service ProxyDomainService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !emptyRequestBody(w, r) {
			return
		}
		name, _, err := domain.NormalizeResource(r.PathValue("domain"))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_domain", "resource path parameter must be a valid DNS name or IPv4 address; IPv6 and CIDR are unsupported")
			return
		}
		if service == nil {
			writeProxyDomainUnavailable(w)
			return
		}
		if err := service.Delete(r.Context(), name); err != nil {
			if errors.Is(err, coordinator.ErrRecoveryBlocked) {
				writeRecoveryBlocked(w)
				return
			}
			writeProxyDomainMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, api.ProxyDomainMutationResponse{Domain: name, Applied: true})
	}
}

func applyProxyDomains(service ProxyDomainService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !emptyRequestBody(w, r) {
			return
		}
		if service == nil {
			writeProxyDomainUnavailable(w)
			return
		}
		if err := service.Apply(r.Context()); err != nil {
			if errors.Is(err, coordinator.ErrRecoveryBlocked) {
				writeRecoveryBlocked(w)
				return
			}
			writeAPIError(w, http.StatusServiceUnavailable, "domain_runtime_apply_failed", "proxy-domain runtime synchronization failed")
			return
		}
		writeJSON(w, http.StatusOK, api.ApplyProxyDomainsResponse{Applied: true})
	}
}

func writeRecoveryBlocked(w http.ResponseWriter) {
	writeAPIError(w, http.StatusServiceUnavailable, "proxy_domain_unavailable", "proxy-domain service is unavailable")
}

func emptyRequestBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return true
	}
	if trimmed[0] != '{' {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body must contain one JSON object")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	_, ok := decodeBody[struct{}](w, r)
	return ok
}

func proxyDomainsResponse(snapshot domainstore.Snapshot) api.ProxyDomainsResponse {
	return api.ProxyDomainsResponse{
		Desired:             snapshot.Desired,
		Baseline:            snapshot.Baseline,
		ConsolidationActive: snapshot.ConsolidationActive,
	}
}

func writeProxyDomainUnavailable(w http.ResponseWriter) {
	writeAPIError(w, http.StatusServiceUnavailable, "proxy_domain_unavailable", "proxy-domain service is unavailable")
}

func writeProxyDomainMutationError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrConsolidationActive) {
		writeAPIError(w, http.StatusConflict, "consolidation_active", "proxy-domain consolidation is active")
		return
	}
	var persistedErr *domainservice.PossiblyPersistedError
	if errors.As(err, &persistedErr) {
		writeAPIError(w, http.StatusServiceUnavailable, persistedErr.Code(), "proxy-domain state may be persisted but runtime synchronization failed")
		return
	}
	writeProxyDomainUnavailable(w)
}
