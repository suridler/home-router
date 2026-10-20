package httpapi

import (
	"net/http"
	"strings"

	"home-router/xray-core-helper/internal/safeapply"
)

// Reads and explicit recovery/status operations remain available. Awaiting a
// manual reboot must not be bypassable merely by skipping disabled GUI buttons.
func rebootGate(next http.Handler, service ApplyTemplateService) http.Handler {
	if service == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || (r.Method == http.MethodPost && (r.URL.Path == "/v1/errors/collect" || r.URL.Path == "/v1/network/probe")) || r.URL.Path == "/v1/templates/refresh" || strings.HasPrefix(r.URL.Path, "/v1/safe-apply/") {
			next.ServeHTTP(w, r)
			return
		}
		pending, err := service.Pending(r.Context())
		if err != nil {
			writeAPIError(w, 503, "safe_apply_unavailable", "safe apply state cannot be checked")
			return
		}
		if pending != nil && pending.Phase == safeapply.PhaseAwaitingReboot {
			writeApplyTemplateError(w, safeapply.ErrRebootRequired)
			return
		}
		if pending != nil {
			writeAPIError(w, 409, "safe_apply_pending", "configuration changes blocked until the pending operation is resolved; retry incomplete recovery before reboot")
			return
		}
		next.ServeHTTP(w, r)
	})
}
