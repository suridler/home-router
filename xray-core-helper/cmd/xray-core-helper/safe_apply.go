package main

import (
	"fmt"
	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/unixhttp"
	"io"
	"net/http"
	"net/url"
	"time"
)

func runSafeApply(arguments []string, configPath string, stdout, stderr io.Writer) int {
	validStatus := len(arguments) == 2 && arguments[1] == "status"
	validResolution := len(arguments) == 3 && (arguments[1] == "confirm" || arguments[1] == "rollback")
	pendingRollback := len(arguments) == 2 && arguments[1] == "rollback-pending"
	if !validStatus && !validResolution && !pendingRollback {
		writeUsage(stderr)
		return 2
	}
	config, err := bootstrap.Load(configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client := unixhttp.NewClient(config.Server.Socket, 30*time.Second)
	if validStatus || pendingRollback {
		var status api.SafeApplyResponse
		if err = requestJSON(client, http.MethodGet, "/v1/safe-apply", nil, &status); err != nil {
			writeRequestError(stderr, "safe-apply", err)
			return 1
		}
		if validStatus {
			if status.Pending != nil && status.Pending.RebootRequired {
				fmt.Fprintln(stderr, "REBOOT REQUIRED: system configuration files restored; runtime effects are NOT restored. Reboot the server manually.")
			}
			if status.Pending != nil && status.Pending.RecoveryRequired {
				fmt.Fprintln(stderr, "RECOVERY INCOMPLETE: full file restoration is not confirmed. Retry rollback; do not reboot until restoration completes. Missing historical baseline requires operator recovery.")
			}
			if err = writeJSON(stdout, status); err != nil {
				return 1
			}
			return 0
		}
		if status.Pending == nil {
			return 0
		}
		arguments = []string{"safe-apply", "rollback", status.Pending.TransactionID}
		validResolution = true
	}
	if validResolution {
		var response api.SafeApplyResolutionResponse
		path := "/v1/safe-apply/" + url.PathEscape(arguments[2]) + "/" + arguments[1]
		if err = requestJSON(client, http.MethodPost, path, struct{}{}, &response); err != nil {
			writeRequestError(stderr, "safe-apply", err)
			return 1
		}
		if !pendingRollback {
			if err = writeJSON(stdout, response); err != nil {
				return 1
			}
		}
		if response.Transaction.RebootRequired {
			fmt.Fprintln(stderr, "REBOOT REQUIRED: system configuration files restored; runtime effects are NOT restored. Reboot the server manually.")
		}
		return 0
	}
	return 2
}
