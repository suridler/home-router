package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const version = "0.3.4"

func main() {
	var (
		socketPath      = flag.String("socket", "/run/xray-routing-exporter/routing-webhook.sock", "Unix socket for Xray Routing Webhook")
		httpAddr        = flag.String("http", "127.0.0.1:9101", "HTTP listen address for API and Prometheus metrics")
		dbPath          = flag.String("db", "/var/lib/xray-routing-exporter/routing.db", "SQLite database path")
		retention       = flag.Duration("retention", 30*24*time.Hour, "SQLite retention window")
		flushEvery      = flag.Duration("flush", 5*time.Second, "How often pending SQLite aggregates are flushed")
		logLevel        = flag.String("log-level", "info", "Log level: debug, info, warn, error")
		helperSocket    = flag.String("helper-socket", "/run/xray-core-helper/helper.sock", "xray-core-helper Unix socket")
		metadataCache   = flag.String("helper-metadata-cache", "/var/lib/xray-routing-exporter/helper-metadata.json", "Last valid helper metadata snapshot")
		metadataRefresh = flag.Duration("helper-refresh", time.Minute, "Successful helper metadata refresh interval")
		xrayStatsEvery  = flag.Duration("xray-stats-interval", 15*time.Second, "Xray StatsService polling interval")
		eventBufferSize = flag.Int("event-buffer-size", 2000, "Number of individual routing events kept in RAM for the events API")
	)
	flag.Parse()

	logger := newLogger(*logLevel)
	slog.SetDefault(logger)

	logger.Info("starting xray-routing-exporter",
		"version", version,
		"socket", *socketPath,
		"http", *httpAddr,
		"db", *dbPath,
	)

	store, err := NewStore(*dbPath, *retention, *flushEvery)
	if err != nil {
		logger.Error("open store", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	metrics := NewMetrics()
	aggregator := NewAggregator(store, metrics)
	eventBuffer := NewEventBuffer(*eventBufferSize)

	if err := aggregator.Start(context.Background()); err != nil {
		logger.Error("start aggregator", "err", err)
		os.Exit(1)
	}
	defer aggregator.Stop()

	helperClient, err := NewHelperClient(*helperSocket, 5*time.Second)
	if err != nil {
		logger.Error("configure helper client", "err", err)
		os.Exit(1)
	}
	cache, err := NewFileMetadataCache(*metadataCache)
	if err != nil {
		logger.Error("configure helper metadata cache", "err", err)
		os.Exit(1)
	}
	metadata, err := NewMetadataManager(helperClient, cache, *metadataRefresh, time.Second, time.Minute)
	if err != nil {
		logger.Error("configure helper metadata manager", "err", err)
		os.Exit(1)
	}
	initializeCtx, cancelInitialize := context.WithTimeout(context.Background(), 5*time.Second)
	if err := metadata.Initialize(initializeCtx, time.Now()); err != nil {
		logger.Warn("helper metadata unavailable at startup; StatsService polling is not ready", "err", err)
	}
	cancelInitialize()

	xrayStats := NewXrayStatsCollector(metadata, *xrayStatsEvery, store, metrics, logger)
	if err := xrayStats.Initialize(); err != nil {
		logger.Error("initialize xray stats state", "err", err)
		os.Exit(1)
	}
	xrayStats.Start(context.Background())
	defer xrayStats.Stop()

	mux := http.NewServeMux()
	registerRoutes(mux, aggregator, store, metrics, metadata, xrayStats, eventBuffer)

	httpServer := &http.Server{
		Addr:              *httpAddr,
		Handler:           requestLoggingMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	webhookHandler := NewWebhookHandler(aggregator, eventBuffer, metrics)
	unixServer := &http.Server{
		Handler:           webhookHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	if err := os.MkdirAll(filepath.Dir(*socketPath), 0o755); err != nil {
		logger.Error("create socket directory", "err", err)
		os.Exit(1)
	}

	if err := removeStaleSocket(*socketPath); err != nil {
		logger.Error("prepare socket", "err", err)
		os.Exit(1)
	}

	unixListener, err := net.Listen("unix", *socketPath)
	if err != nil {
		logger.Error("listen unix socket", "err", err)
		os.Exit(1)
	}
	if err := os.Chmod(*socketPath, 0o660); err != nil {
		logger.Error("chmod unix socket", "err", err)
		unixListener.Close()
		os.Exit(1)
	}

	logger.Info("webhook socket ready", "path", *socketPath)

	errCh := make(chan error, 2)
	go func() {
		if err := unixServer.Serve(unixListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("webhook server: %w", err)
		}
	}()
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	sigCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	select {
	case <-sigCtx.Done():
		logger.Info("shutdown requested")
	case err := <-errCh:
		logger.Error("server stopped unexpectedly", "err", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = unixServer.Shutdown(shutdownCtx)
	_ = httpServer.Shutdown(shutdownCtx)
	_ = unixListener.Close()
	_ = os.Remove(*socketPath)

	logger.Info("shutdown complete")
}

func newLogger(level string) *slog.Logger {
	var slogLevel slog.Level
	switch level {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slogLevel}))
}

func removeStaleSocket(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to remove non-socket path %s", path)
	}
	return os.Remove(path)
}
