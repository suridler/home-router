package main

import (
	"context"
	"fmt"
	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/safeapply"
	"home-router/xray-core-helper/internal/selfupdate"
	"home-router/xray-core-helper/internal/templateapply"
	"home-router/xray-core-helper/internal/templatebackup"
	"home-router/xray-core-helper/internal/unixhttp"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

func runSafeApply(arguments []string, configPath string, stdout, stderr io.Writer) int {
	if len(arguments) == 4 && arguments[1] == "cancel-reboot" && arguments[3] == "--offline" {
		return cancelRebootOffline(arguments[2], configPath, stdout, stderr)
	}
	validStatus := len(arguments) == 2 && arguments[1] == "status"
	validResolution := len(arguments) == 3 && (arguments[1] == "confirm" || arguments[1] == "rollback" || arguments[1] == "cancel-reboot")
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
		if arguments[1] == "cancel-reboot" {
			fmt.Fprintln(stderr, "Снимается требование перезагрузки. Файлы и работающая сеть не изменяются; проверка runtime остаётся за оператором.")
		}
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

func cancelRebootOffline(id, configPath string, stdout, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "offline recovery requires root")
		return 1
	}
	release, err := selfupdate.Lock(runtimeLockPath)
	if err != nil {
		fmt.Fprintln(stderr, "Остановите helper перед --offline; runtime lock занят.")
		return 1
	}
	defer release()
	cfg, err := bootstrap.Load(configPath)
	if err != nil {
		fmt.Fprintln(stderr, "could not load helper settings")
		return 1
	}
	ctx := context.Background()
	store, err := safeapply.Open(ctx, cfg.Storage.Database)
	if err != nil {
		fmt.Fprintln(stderr, "could not open safe-apply journal")
		return 1
	}
	defer store.Close()
	backups, err := templatebackup.New(func() int { return cfg.Backup.RetainPerTemplate }, nil)
	if err != nil {
		fmt.Fprintln(stderr, "could not open backup metadata")
		return 1
	}
	timer, err := safeapply.NewSystemdTimer()
	if err != nil {
		fmt.Fprintln(stderr, "could not initialize rollback timer")
		return 1
	}
	complete := func(ctx context.Context, d templateapply.ConfirmedDescriptor) error {
		_, err := backups.SetState(ctx, d.TemplateID, d.BackupID, templatebackup.StateRollback)
		return err
	}
	value, changed, err := safeapply.CancelRebootRecovery(ctx, store, timer, id, complete)
	if err != nil {
		fmt.Fprintln(stderr, "cancel-reboot:", err)
		return 1
	}
	fmt.Fprintln(stderr, "Требование reboot снято по решению оператора. Файлы, маршруты и службы Xray не изменены. Helper не запускался.")
	return writeJSONCode(stdout, api.SafeApplyResolutionResponse{Transaction: api.SafeApplyTransaction{TransactionID: value.ID, TemplateID: string(value.TemplateID), Deadline: value.Deadline, Phase: string(value.Phase), State: string(value.State), Generation: value.Generation}, Changed: changed})
}
