package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/unixhttp"
)

func runApplyTemplate(arguments []string, configPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	force := false
	filtered := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		if argument == "-force" || argument == "--force" {
			if force {
				writeUsage(stderr)
				return 2
			}
			force = true
		} else {
			filtered = append(filtered, argument)
		}
	}
	arguments = filtered
	if (len(arguments) != 5 && len(arguments) != 8) || arguments[3] != "--expected-generation" {
		writeUsage(stderr)
		return 2
	}
	generation, err := strconv.ParseUint(arguments[4], 10, 64)
	if err != nil {
		writeUsage(stderr)
		return 2
	}
	config, err := bootstrap.Load(configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client := unixhttp.NewClient(config.Server.Socket, 30*time.Second)
	path := "/v1/templates/" + url.PathEscape(arguments[2]) + "/apply"
	request := api.ApplyTemplateRequest{ExpectedGeneration: &generation, Force: force}
	if len(arguments) == 8 {
		if arguments[2] != "nftables" || arguments[5] != "--initialize-block" || arguments[6] != "--expected-revision" || len(arguments[7]) != 64 {
			writeUsage(stderr)
			return 2
		}
		request.InitializeBlock, request.ExpectedRevision = true, arguments[7]
		fmt.Fprintln(stderr, "INITIALIZE: operator review of existing content/diff is required; append only, semantic rule compatibility is unchecked.")
	}
	if arguments[2] == "nftables" || arguments[2] == "sysctl" || arguments[2] == "tproxy-unit" {
		fmt.Fprintln(stderr, "WARNING: rollback restores nftables, sysctl and systemd files from one baseline; runtime effects require a MANUAL SERVER REBOOT. No automatic reboot will occur. Keep console access available.")
	}
	if force {
		fmt.Fprintln(stderr, "FORCE: изменения применяются без таймера отката и без запроса подтверждения.")
	}
	if arguments[2] == "tproxy-unit" {
		fmt.Fprintln(stderr, "Применение TProxy-unit перезапустит Xray; соединения могут прерваться.")
	}
	var response api.ApplyTemplateResponse
	if err := requestJSON(client, http.MethodPost, path, request, &response); err != nil {
		writeRequestError(stderr, "templates", err)
		fmt.Fprintln(stderr, "Перед повтором проверьте: sudo xray-core-helper safe-apply status")
		return 1
	}
	if force && response.State != "confirmed" {
		fmt.Fprintln(stderr, "Helper не подтвердил завершение принудительного применения; проверьте safe-apply status.")
		return 1
	}
	if !force && response.TransactionID != "" {
		return confirmTemplateApply(client, bufio.NewScanner(stdin), stdout, stderr, response)
	}
	if err := writeJSON(stdout, response); err != nil {
		fmt.Fprintln(stderr, "could not write command output")
		return 1
	}
	return 0
}

// The server owns the deadline. Polling checks the connection and transaction;
// neither keyboard activity nor heartbeat renews the rollback timer.
func confirmTemplateApply(client *http.Client, input *bufio.Scanner, stdout, stderr io.Writer, applied api.ApplyTemplateResponse) int {
	pending := func(message string) int {
		fmt.Fprintln(stderr, "\n"+message)
		fmt.Fprintln(stderr, "Подтверждение не отправлено. Проверьте: sudo xray-core-helper safe-apply status")
		return 1
	}
	if applied.Deadline.IsZero() || !time.Now().Before(applied.Deadline) {
		return pending("Срок подтверждения истёк или не получен.")
	}
	fmt.Fprintf(stderr, "Изменения применены временно. Срок подтверждения: %s.\nПроверьте доступ с сервера и LAN. Таймер отката не продлевается.\n",
		applied.Deadline.Local().Format("02.01.2006 15:04:05 MST"))
	probe := func() {
		printNetworkProbe(client, stderr, api.NetworkProbeRequest{Stage: "after", TransactionID: applied.TransactionID})
	}
	probe()
	lastResponse := time.Time{}
	heartbeat := func() error {
		// Keep the live indicator bounded even if the helper stops responding.
		checkClient := *client
		checkClient.Timeout = min(2*time.Second, time.Until(applied.Deadline))
		if checkClient.Timeout <= 0 {
			return fmt.Errorf("confirmation_deadline_expired")
		}
		var status api.SafeApplyResponse
		if err := requestJSON(&checkClient, http.MethodGet, "/v1/safe-apply", nil, &status); err != nil {
			return err
		}
		if status.Pending == nil || status.Pending.TransactionID != applied.TransactionID || status.Pending.Phase != "applied" || status.Pending.State != "pending" {
			return fmt.Errorf("safe_apply_no_longer_pending")
		}
		lastResponse = time.Now()
		return nil
	}
	if err := heartbeat(); err != nil {
		return pending("Связь с helper или ожидающее состояние транзакции не подтверждены.")
	}
	fmt.Fprintln(stderr, "Сохранить изменения? Можно подтвердить и при неуспешной проверке сети.\n  1) Да, сохранить\n  2) Откатить изменения\n  3) Повторить проверку сети\n  4) Оставить таймер отката активным")
	prompt := confirmationPrompt{writer: stderr, terminal: confirmationTerminal(stderr)}
	display := func() {
		remaining := int(time.Until(applied.Deadline).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		prompt.update(fmt.Sprintf("Осталось %02d:%02d · ответ helper %s", remaining/60, remaining%60, lastResponse.Format("15:04:05")))
	}
	// Reading input never writes to the terminal; the main loop owns display.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines := make(chan string)
	go func() {
		defer close(lines)
		for input.Scan() {
			select {
			case lines <- input.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	ticks := time.NewTicker(time.Second)
	defer ticks.Stop()
	checks := time.NewTicker(5 * time.Second)
	defer checks.Stop()
	deadline := time.NewTimer(time.Until(applied.Deadline))
	defer deadline.Stop()
	display()
	for {
		select {
		case <-deadline.C:
			return pending("Срок подтверждения истёк; проверьте результат отката.")
		case <-ticks.C:
			display()
		case <-checks.C:
			if err := heartbeat(); err != nil {
				return pending("Связь с helper потеряна либо транзакция уже перешла в другое состояние.")
			}
			display()
		case line, ok := <-lines:
			if !ok {
				return pending("Ввод завершён; таймер отката остаётся активным.")
			}
			fmt.Fprintln(stderr)
			prompt.started = false
			if !time.Now().Before(applied.Deadline) {
				return pending("Срок подтверждения истёк.")
			}
			line = strings.TrimSpace(line)
			if line == "" || line == "4" {
				return pending("Команда завершена без подтверждения; таймер отката остаётся активным.")
			}
			if line == "3" {
				probe()
				display()
				continue
			}
			if line != "1" && line != "2" {
				fmt.Fprintln(stderr, "Введите номер одного из вариантов.")
				display()
				continue
			}
			// Verify again immediately before resolving, even between heartbeats.
			if err := heartbeat(); err != nil {
				return pending("Связь с helper или ожидающее состояние транзакции не подтверждены.")
			}
			action := "confirm"
			if line == "2" {
				action = "rollback"
			}
			var result api.SafeApplyResolutionResponse
			path := "/v1/safe-apply/" + url.PathEscape(applied.TransactionID) + "/" + action
			if err := requestJSON(client, http.MethodPost, path, struct{}{}, &result); err != nil {
				writeRequestError(stderr, "safe-apply; проверьте safe-apply status перед повтором", err)
				return 1
			}
			if action == "confirm" && result.Transaction.State != "confirmed" {
				return pending("Helper не подтвердил сохранение изменений.")
			}
			if result.Transaction.RebootRequired {
				fmt.Fprintln(stderr, "Файлы восстановлены; runtime не восстановлен. Не перезагружайте удалённый сервер без независимой консоли и проверки загрузочных файлов.")
			} else if action == "confirm" {
				fmt.Fprintln(stderr, "Изменения подтверждены и сохранены.")
			}
			return writeJSONCode(stdout, result)
		}
	}
}
