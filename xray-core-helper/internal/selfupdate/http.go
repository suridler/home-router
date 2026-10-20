package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"home-router/xray-core-helper/internal/coordinator"
)

type Request struct {
	Version     string `json:"version,omitempty"`
	OperationID string `json:"operationId,omitempty"`
	ActionID    string `json:"actionId,omitempty"`
}

// Wrap intercepts only a closed set of lifecycle operations. No request can
// choose an executable, installation root, service unit or filesystem path.
func (e *Engine) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/update") {
			if r.URL.Path == "/v1/update" && r.Method == "GET" {
				op, err := e.Status(r.Context())
				if err != nil {
					updateError(w, err)
					return
				}
				m, err := e.manifest()
				if err != nil {
					updateError(w, err)
					return
				}
				respond(w, 200, map[string]any{"currentVersion": m.Version, "currentCommit": m.Commit, "operation": op.Public()})
				return
			}
			if r.Method != "POST" {
				respondError(w, 405, "method_not_allowed", "method not allowed")
				return
			}
			var input Request
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			d.DisallowUnknownFields()
			if err := d.Decode(&input); err != nil {
				respondError(w, 400, "invalid_request", "one update request object required")
				return
			}
			var extra any
			if d.Decode(&extra) != io.EOF {
				respondError(w, 400, "invalid_request", "trailing request content")
				return
			}
			if r.URL.Path == "/v1/update/check" {
				if input != (Request{}) {
					respondError(w, 400, "invalid_request", "empty object required")
					return
				}
				out, err := e.CheckVersion(r.Context())
				if err != nil {
					updateError(w, err)
					return
				}
				respond(w, 200, out)
				return
			}
			if r.URL.Path != "/v1/update/prepare" && r.URL.Path != "/v1/update/apply" && r.URL.Path != "/v1/update/retry" && r.URL.Path != "/v1/update/rollback" {
				respondError(w, 404, "not_found", "endpoint not found")
				return
			}
			// Interrupted upgrade recovery is permitted independently of normal
			// database/config recovery gates; it restores their matching snapshot.
			var release func()
			if r.URL.Path == "/v1/update/prepare" || r.URL.Path == "/v1/update/apply" {
				lease, err := coordinator.Acquire(r.Context())
				if err != nil {
					updateError(w, err)
					return
				}
				release = lease.Release
				defer release()
			}
			var op *Operation
			var err error
			if r.URL.Path == "/v1/update/prepare" {
				if input.OperationID != "" || input.ActionID != "" {
					respondError(w, 400, "invalid_request", "prepare accepts version only")
					return
				}
				op, err = e.Prepare(r.Context(), input.Version, nil)
			} else {
				if input.Version != "" || requiredID(input.OperationID) != nil || requiredID(input.ActionID) != nil {
					respondError(w, 400, "invalid_request", "operationId and actionId required")
					return
				}
				op, err = e.Request(r.Context(), input.OperationID, strings.TrimPrefix(r.URL.Path, "/v1/update/"), input.ActionID)
			}
			if err != nil {
				updateError(w, err)
				return
			}
			respond(w, 200, map[string]any{"operation": op.Public()})
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			op, err := e.Store.Load()
			if err != nil {
				respondError(w, 503, "update_state_unavailable", "installation journal unavailable")
				return
			}
			if op != nil && op.State != "prepared" && !terminal(op.State) {
				respondError(w, 409, "update_pending", "installation or recovery pending; inspect update status")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func respond(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func respondError(w http.ResponseWriter, status int, code, message string) {
	respond(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func updateError(w http.ResponseWriter, err error) {
	code := errorCode(err)
	status := 409
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code = "update_timeout"
		status = 504
	}
	if strings.HasPrefix(err.Error(), "invalid ") {
		status = 400
		code = "invalid_update_request"
	}
	if errors.Is(err, coordinator.ErrRecoveryBlocked) {
		code = "recovery_pending"
	}
	respondError(w, status, code, "Операция обновления не выполнена ("+code+"). Проверьте статус установки.")
}

// Quarantined allows the daemon to serve recovery even when a partial install
// left configuration/database initialization unusable. Verification, in contrast,
// must construct the real application before claiming success.
func (e *Engine) Quarantined(ctx context.Context) (bool, error) {
	op, err := e.Status(ctx)
	if err != nil {
		return false, err
	}
	return op != nil && (op.State == "failed" || op.State == "interrupted" || op.State == "applying" || op.State == "launching"), nil
}

func (e *Engine) MaintenanceServer(version string, webSettings any) *http.Server {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/v1/health" {
			respond(w, 200, map[string]string{"status": "recovery_required", "version": version})
			return
		}
		if r.Method == "GET" && r.URL.Path == "/v1/web/settings" {
			respond(w, 200, webSettings)
			return
		}
		respondError(w, 503, "update_recovery_required", "Complete installation recovery before ordinary operations")
	})
	return &http.Server{Handler: e.Wrap(h), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Minute, IdleTimeout: 30 * time.Second}
}
