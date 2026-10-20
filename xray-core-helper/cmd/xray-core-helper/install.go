package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/helperapp"
	"home-router/xray-core-helper/internal/selfupdate"
	"home-router/xray-core-helper/internal/unixhttp"
)

func runUpdate(args []string, config string, out, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "update check|status|prepare [VERSION]|apply|retry|rollback OPERATION_ID ACTION_ID")
		return 2
	}
	cfg, err := bootstrap.Load(config)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client := unixhttp.NewClient(cfg.Server.Socket, 15*time.Minute)
	method, path := http.MethodPost, "/v1/update/"+args[0]
	var input any = selfupdate.Request{}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return 2
		}
		method, path, input = http.MethodGet, "/v1/update", nil
	case "check":
		if len(args) != 1 {
			return 2
		}
	case "prepare":
		if len(args) > 2 {
			return 2
		}
		v := ""
		if len(args) == 2 {
			v = args[1]
		}
		input = selfupdate.Request{Version: v}
	case "apply", "retry", "rollback":
		if len(args) != 3 {
			return 2
		}
		input = selfupdate.Request{OperationID: args[1], ActionID: args[2]}
	default:
		return 2
	}
	var result any
	if err = requestJSON(client, method, path, input, &result); err != nil {
		writeRequestError(stderr, "update", err)
		return 1
	}
	return writeJSONCode(out, result)
}

func runInstall(args []string, config string, in io.Reader, out, stderr io.Writer) int {
	f := flag.NewFlagSet("install", flag.ContinueOnError)
	f.SetOutput(stderr)
	target := f.String("version", "", "release version")
	local := f.String("bundle", "", "operator-provided unpacked release directory")
	recover := f.Bool("recover", false, "inspect interrupted installation and choose retry/rollback")
	withWeb := f.Bool("with-web", false, "install web on a fresh host")
	withoutWeb := f.Bool("without-web", false, "install CLI only on a fresh host")
	worker := f.String("worker", "", "execute saved operation")
	attempt := f.String("attempt", "", "saved attempt ID")
	if f.Parse(args) != nil || len(f.Args()) != 0 || (*withWeb && *withoutWeb) || (*recover && (*target != "" || *local != "")) || (*local != "" && *target != "") {
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "Установщик требует sudo.")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	e := selfupdate.New()
	e.ConfigPath = config
	e.InstallerSource, _ = os.Executable()
	interactive := false
	if file, ok := in.(*os.File); ok {
		if st, err := file.Stat(); err == nil {
			interactive = st.Mode()&os.ModeCharDevice != 0
		}
	}
	e.Setup = func(ctx context.Context) error {
		if !interactive {
			fmt.Fprintln(out, "Компоненты установлены. Для настройки выполните: sudo xray-core-helper setup")
			return nil
		}
		client, close, err := helperapp.SetupClient(ctx, e.ConfigPath)
		if err != nil {
			return err
		}
		defer close()
		if runSetupWithClient(client, []string{"setup"}, in, out, stderr) != 0 {
			return errors.New("standalone_setup_failed")
		}
		return nil
	}
	if *worker != "" {
		if *attempt == "" || *recover || *target != "" || *local != "" || *withWeb || *withoutWeb {
			return 2
		}
		if err := e.Run(ctx, *worker, *attempt); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if *attempt != "" {
		return 2
	}
	op, err := e.Status(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if op != nil && op.State != "succeeded" && op.State != "rolled_back" {
		if op.State == "launching" || op.State == "applying" || op.State == "verifying" || op.State == "preparing" {
			fmt.Fprintln(stderr, "Операция ещё выполняется или её состояние не подтверждено; проверьте update status.")
			return 1
		}
		fmt.Fprintf(out, "Незавершённая установка %s: %s (%s), этап %s.\n", op.TargetVersion, op.State, op.ID, op.Stage)
		options := []string{"Повторить установку выбранной версии", "Отложить"}
		rollbackChoice := 0
		if op.SnapshotReady {
			options = []string{"Повторить установку выбранной версии", "Откатить предыдущий комплект и данные", "Отложить"}
			rollbackChoice = 2
		}
		choice, ok := choose(bufio.NewScanner(in), out, "Выберите действие восстановления:", options, len(options))
		if !ok || choice == len(options) {
			return 0
		}
		action := "retry"
		if choice == rollbackChoice {
			action = "rollback"
		}
		if op.State == "prepared" {
			action = "apply"
		}
		if err = e.Direct(ctx, op.ID, action); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(out, "Операция завершена.")
		return 0
	}
	if *recover {
		fmt.Fprintln(out, "Незавершённой установки нет.")
		return 0
	}
	var web *bool
	if *withWeb || *withoutWeb {
		v := *withWeb
		web = &v
	} else if op == nil {
		if _, err = os.Stat("/usr/local/sbin/xray-core-helperd"); os.IsNotExist(err) && interactive {
			choice, ok := choose(bufio.NewScanner(in), out, "Установить web-панель?", []string{"Да", "Нет, только CLI"}, 1)
			if !ok {
				return 1
			}
			v := choice == 1
			web = &v
		}
	}
	if *local != "" {
		op, err = e.PrepareLocal(ctx, *local, web)
	} else {
		op, err = e.Prepare(ctx, *target, web)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(out, "Подготовлен комплект %s.\n", op.TargetVersion)
	if err = e.Direct(ctx, op.ID, "apply"); err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, "Для восстановления повторите bootstrap с --recover.")
		return 1
	}
	fmt.Fprintln(out, "Установка завершена.")
	return 0
}
