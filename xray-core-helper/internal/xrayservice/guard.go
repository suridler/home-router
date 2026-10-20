package xrayservice

import (
	"context"
	"net/http"
	"time"
)

// Guard blocks new HTTP mutations when effective service paths have changed.
// Reads remain available; no recovery evidence or filesystem grants are edited.
func Guard(next http.Handler, expected Installation, resolve func(context.Context) (Installation, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
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
