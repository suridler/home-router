package httpapi

import (
	"context"
	"net/http"
	"strings"

	"home-router/xray-core-helper/internal/safeapply"
	"home-router/xray-core-helper/internal/templatecatalog"
)

// IndependentNativeOperation identifies operations that do not write the Xray
// config. Recovery uses the recorded template, never a client-supplied type.
func IndependentNativeOperation(r *http.Request, lookup func(context.Context, string) (safeapply.Transaction, error)) bool {
	if r.Method != http.MethodPost {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/v1/templates/") {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/templates/"), "/")
		if !nativeTemplate(templatecatalog.ID(parts[0])) {
			return false
		}
		return len(parts) == 2 && (parts[1] == "apply" || parts[1] == "accept-system") ||
			len(parts) == 4 && parts[1] == "backups" && parts[2] != "" && parts[3] == "restore"
	}
	if strings.HasPrefix(r.URL.Path, "/v1/safe-apply/") && lookup != nil {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/safe-apply/"), "/")
		if len(parts) != 2 || parts[0] == "" || (parts[1] != "confirm" && parts[1] != "rollback") {
			return false
		}
		transaction, err := lookup(r.Context(), parts[0])
		return err == nil && transaction.ID == parts[0] && nativeTemplate(transaction.TemplateID)
	}
	return false
}

func nativeTemplate(id templatecatalog.ID) bool {
	return id == templatecatalog.Nftables || id == templatecatalog.Sysctl || id == templatecatalog.TProxyUnit
}
