package xrayservice

import (
	"context"
	"net/http"
	"time"
)

// Guard checks service paths before operations that depend on the installed
// Xray paths. Read-only probes and helper-local collection remain available.
func Guard(next http.Handler, expected Installation, resolve func(context.Context) (Installation, error), independent func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requiresServicePaths(r) && (independent == nil || !independent(r)) {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			actual, err := resolve(ctx)
			cancel()
			if err != nil || actual != expected {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":{"code":"xray_service_paths_changed","message":"Review service paths and rerun installation before mutations; retain recovery evidence"}}`))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func requiresServicePaths(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return false
	}
	// These fixed maintenance endpoints do not change Xray or system files.
	// Transaction-specific exceptions are supplied by the owning API.
	if r.Method == http.MethodPost {
		switch r.URL.Path {
		case "/v1/errors/collect", "/v1/network/probe", "/v1/templates/refresh":
			return false
		}
	}
	return true
}
