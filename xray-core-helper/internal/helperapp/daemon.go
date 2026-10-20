package helperapp

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/loggingservice"
	"home-router/xray-core-helper/internal/scheduler"
	"home-router/xray-core-helper/internal/selfupdate"
	"home-router/xray-core-helper/internal/webapp"
)

// Run starts the normal helper daemon. Setup-required and recovery modes keep
// their existing Unix-socket behavior; a completed setup hands off by restarting
// composition in normal mode.
func Run(configPath, version string) error {
	release, err := selfupdate.Lock(selfupdate.StateRoot + "/runtime.lock")
	if err != nil {
		return err
	}
	defer release()
	updater := selfupdate.New()
	updater.ConfigPath = configPath
	quarantined, err := updater.Quarantined(context.Background())
	if err != nil {
		return err
	}
	if quarantined {
		return serveUpdateRecovery(configPath, version, updater)
	}
	return runWithLifecycle(configPath, version, systemRuntimeLifecycle())
}

func runWithLifecycle(configPath, version string, lifecycle runtimeLifecycle) error {
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStartup()
	app, err := openApplication(startupCtx, configPath, version, true)
	if err != nil {
		return err
	}
	if err := app.prepareSetup(startupCtx); err != nil {
		return err
	}
	configProbe := app.setupStartup.Status()
	if !configProbe.RecoveryRequired {
		// Setup recovery may have restored the previous network selection.
		if err := app.restoreNetwork(); err != nil {
			return err
		}
	}
	if configProbe.RecoveryRequired || configProbe.SetupRequired {
		if err := app.runSetupDaemon(startupCtx, lifecycle); errors.Is(err, errSetupCompleted) {
			return runWithLifecycle(configPath, version, lifecycle)
		} else {
			return err
		}
	}
	setupState := app.normalSetupService()
	if setupState.Status().RecoveryRequired {
		return app.runSetupDaemon(startupCtx, lifecycle)
	}
	if err = app.webRuntime.Prepare(startupCtx, false); err != nil {
		return err
	}
	normal, err := app.buildNormal(startupCtx, setupState)
	if err != nil {
		return err
	}
	defer normal.Close()
	return app.serveNormal(startupCtx, normal, lifecycle)
}

type daemonRuntime struct {
	logMutations *loggingservice.Service
}

func (runtime daemonRuntime) serve(server *http.Server, listener net.Listener) error {
	if runtime.logMutations == nil {
		return errors.New("logging mutation service is unavailable")
	}
	return server.Serve(listener)
}

func (a *application) serveNormal(startupCtx context.Context, normal *normalBundle, lifecycle runtimeLifecycle) error {
	listener, err := lifecycle.listen(a.config.Server.Socket, a.config.Server.SocketGroup)
	if err != nil {
		return err
	}
	defer listener.Close()
	updater := selfupdate.New()
	updater.ConfigPath = a.configPath
	normal.server.Handler = updater.Wrap(normal.server.Handler)
	runtime := daemonRuntime{logMutations: normal.logMutations}
	serveResult := make(chan error, 1)
	go func() { serveResult <- runtime.serve(normal.server, listener) }()
	if a.webEnabled {
		if err = a.webRuntime.Reconcile(startupCtx); err != nil {
			_ = normal.server.Close()
			return err
		}
	}
	if normal.setupState != nil && normal.setupState.HandoffPending() {
		if err = normal.setupState.CompleteHandoff(startupCtx); err != nil {
			_ = normal.server.Close()
			return errors.New("setup handoff status failed")
		}
	}

	ctx, stop := lifecycle.notify(context.Background())
	defer stop()
	jobs := scheduler.MaintenanceJobs(a.config.Server.Socket)
	if a.config.Scheduler.GeodataUpdateEnabled && geodataSourcesEnabled(a.config.Geodata) {
		scheduleStore, err := scheduler.Open(ctx, a.config.Storage.Database)
		if err != nil {
			_ = normal.server.Close()
			return errors.New("scheduler state unavailable")
		}
		defer scheduleStore.Close()
		hour, minute := lifecycle.parseHHMM(a.config.Scheduler.GeodataUpdateTime)
		job := scheduler.GeodataJob(a.config.Server.Socket, hour, minute)
		jobs = append(jobs, scheduleStore.Persist(job, "geodata-"+a.config.Scheduler.GeodataUpdateTime))
	}
	jobCtx, cancelJobs := context.WithCancel(ctx)
	startJobs := lifecycle.startJobs
	if startJobs == nil {
		startJobs = scheduler.Start
	}
	joinJobs := startJobsAfterUpdate(jobCtx, updater, func() func() {
		return startJobs(jobCtx, jobs, func(name string, failed bool) {
			if failed {
				log.Printf("scheduled %s: failed; inspect helper status", name)
			} else {
				log.Printf("scheduled %s: recovered", name)
			}
		})
	})
	defer func() { cancelJobs(); joinJobs() }()
	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		cancelJobs()
		joinJobs()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := normal.server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-serveResult
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
}

func (a *application) runSetupDaemon(ctx context.Context, lifecycle runtimeLifecycle) error {
	if err := a.webRuntime.Prepare(context.Background(), true); err != nil {
		return err
	}
	bundle, err := a.buildSetup()
	if err != nil {
		return err
	}
	updater := selfupdate.New()
	updater.ConfigPath = a.configPath
	original := bundle.server.Handler
	bundle.server.Handler = updater.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"code":"standalone_setup_required","message":"Run sudo xray-core-helper setup; setup requires the daemon to be stopped"}}`))
			return
		}
		original.ServeHTTP(w, r)
	}))
	listener, err := lifecycle.listen(a.config.Server.Socket, a.config.Server.SocketGroup)
	if err != nil {
		return err
	}
	defer listener.Close()
	serveResult := make(chan error, 1)
	go func() { serveResult <- bundle.server.Serve(listener) }()
	if a.webEnabled {
		if err = a.webRuntime.Reconcile(context.Background()); err != nil {
			_ = bundle.server.Close()
			return err
		}
	}
	setupCtx, stop := lifecycle.notify(context.Background())
	defer stop()
	select {
	case <-bundle.service.Completed():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := bundle.server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		<-serveResult
		return errSetupCompleted
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case <-setupCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := bundle.server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-serveResult
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
}

// No maintenance job may mutate data while an installer is verifying or awaiting
// an explicit recovery decision. The wait ends with normal daemon cancellation.
func startJobsAfterUpdate(ctx context.Context, e *selfupdate.Engine, start func() func()) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			op, err := e.Store.Load()
			if err == nil && (op == nil || op.State == "succeeded" || op.State == "rolled_back" || op.State == "prepared") {
				join := start()
				<-ctx.Done()
				join()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	return func() { <-done }
}
func serveUpdateRecovery(configPath, version string, e *selfupdate.Engine) error {
	cfg, err := bootstrap.Load(configPath)
	if err != nil {
		return err
	}
	enabled, err := webapp.Enabled(os.Getenv("XRAY_HELPER_WEB_ENABLED"))
	if err != nil {
		return err
	}
	web, err := webapp.New(cfg.Server.Socket, enabled)
	if err != nil {
		return err
	}
	if err = web.Prepare(context.Background(), false); err != nil {
		return err
	}
	server := e.MaintenanceServer(version, webSettingsResponse(cfg.Web, enabled))
	life := systemRuntimeLifecycle()
	listener, err := life.listen(cfg.Server.Socket, cfg.Server.SocketGroup)
	if err != nil {
		return err
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if enabled {
		if err = web.Reconcile(context.Background()); err != nil {
			_ = server.Close()
			return err
		}
	}
	ctx, stop := life.notify(context.Background())
	defer stop()
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
