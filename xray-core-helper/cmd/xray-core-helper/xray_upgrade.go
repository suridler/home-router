package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"home-router/xray-core-helper/internal/selfupdate"
)

// Worker parameters select only a journaled operation, never paths or commands.
func runXrayUpgrade(args []string, config string, out, stderr io.Writer) int {
	if len(args) != 3 || args[0] != "--worker" {
		fmt.Fprintln(stderr, "xray-upgrade is an internal installer worker; use the web update panel")
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
	if err := e.RunXrayUpgrade(ctx, args[1], args[2]); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
