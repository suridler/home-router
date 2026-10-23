package selfupdate

import (
	"net/http"
	"strings"

	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/jsonrequest"
)

func (e *Engine) xrayUpgradeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/xray-upgrade" && r.Method == http.MethodGet {
		s, err := e.XrayUpgradeStatus(r.Context())
		if err != nil {
			updateError(w, err)
			return
		}
		current := ""
		if _, v, err := e.currentXray(r.Context()); err == nil {
			current = v
		}
		respond(w, 200, map[string]any{"currentVersion": current, "operation": s.Public()})
		return
	}
	if r.Method != http.MethodPost {
		respondError(w, 405, "method_not_allowed", "method not allowed")
		return
	}
	var input Request
	if err := jsonrequest.Decode(http.MaxBytesReader(w, r.Body, 4096), &input); err != nil {
		respondError(w, 400, "invalid_request", "one update request object required")
		return
	}
	if input.Version != "" {
		respondError(w, 400, "invalid_request", "Xray updates use the latest stable release")
		return
	}
	if r.URL.Path == "/v1/xray-upgrade/check" {
		if input != (Request{}) {
			respondError(w, 400, "invalid_request", "empty object required")
			return
		}
		result, err := e.CheckXrayUpgrade(r.Context())
		if err != nil {
			updateError(w, err)
			return
		}
		respond(w, 200, result)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/v1/xray-upgrade/")
	if action != "apply" && action != "retry" && action != "rollback" {
		respondError(w, 404, "not_found", "endpoint not found")
		return
	}
	if input.ActionID == "" || (action == "apply" && input.OperationID != "") || (action != "apply" && requiredID(input.OperationID) != nil) {
		respondError(w, 400, "invalid_request", "valid action and operation ids required")
		return
	}
	lease, err := coordinator.Acquire(r.Context())
	if err != nil {
		updateError(w, err)
		return
	}
	defer lease.Release()
	s, err := e.RequestXrayUpgrade(r.Context(), action, input)
	if err != nil {
		updateError(w, err)
		return
	}
	respond(w, 202, map[string]any{"operation": s.Public()})
}
