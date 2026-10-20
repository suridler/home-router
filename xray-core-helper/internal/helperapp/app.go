// Package helperapp composes the privileged helper application. It owns the
// shared handler/service graph used by the system daemon and by standalone
// setup, without duplicating the typed HTTP handlers.
package helperapp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/httpapi"
	"home-router/xray-core-helper/internal/scheduler"
	"home-router/xray-core-helper/internal/servicecontrol"
	"home-router/xray-core-helper/internal/setup"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatevalidation"
	"home-router/xray-core-helper/internal/unixhttp"
	"home-router/xray-core-helper/internal/webapp"
	"home-router/xray-core-helper/internal/xrayservice"
)

var errSetupCompleted = errors.New("setup completed; hand off into normal mode")

// application is the startup projection shared by daemon and standalone setup.
// It never creates a listener, scheduler or web runtime by itself; those belong
// to the selected run mode.
type application struct {
	config       bootstrap.Config
	configPath   string
	version      string
	installation xrayservice.Installation
	catalog      templatecatalog.Catalog
	webEnabled   bool
	webRuntime   *webapp.Runtime

	validator    *templatevalidation.Registry
	setupStore   setup.SetupStore
	setupStartup *setup.Startup
}

// openApplication resolves the canonical Xray installation and validates the
// helper config. withWeb is false for standalone in-process setup so no web
// service reconciliation or environment file write happens offline.
func openApplication(ctx context.Context, configPath, version string, withWeb bool) (*application, error) {
	config, err := bootstrap.Load(configPath)
	if err != nil {
		return nil, err
	}
	installation, err := xrayservice.New().Resolve(ctx)
	if err != nil {
		return nil, err
	}
	if err := installation.CheckTrusted(); err != nil {
		return nil, err
	}
	config.Xray.Binary, config.Xray.Config, config.Xray.AssetDir = installation.Binary, installation.Config, installation.AssetDir
	if err := config.Validate(); err != nil {
		return nil, err
	}
	catalog, err := templatecatalog.ForXrayConfig(config.Xray.Config)
	if err != nil {
		return nil, errors.New("invalid Xray service config destination")
	}
	webEnabled, err := webapp.Enabled(os.Getenv("XRAY_HELPER_WEB_ENABLED"))
	if err != nil {
		return nil, err
	}
	var webRuntime *webapp.Runtime
	if withWeb {
		webRuntime, err = webapp.New(config.Server.Socket, webEnabled)
		if err != nil {
			return nil, err
		}
		if !webEnabled {
			if err = webRuntime.Reconcile(ctx); err != nil {
				return nil, err
			}
		}
	}
	return &application{
		config:       config,
		configPath:   configPath,
		version:      version,
		installation: installation,
		catalog:      catalog,
		webEnabled:   webEnabled,
		webRuntime:   webRuntime,
	}, nil
}

// prepareSetup creates the setup store and recovers interrupted setup before a
// run mode is selected. A failed restore is represented by startup status so
// read/status and explicit recovery remain available.
func (a *application) prepareSetup(ctx context.Context) error {
	nativeTemplateValidator, err := templatevalidation.NewWithXray(nil, a.config.Xray.Binary, a.config.Xray.AssetDir)
	if err != nil {
		return err
	}
	setupStore, err := setup.NewFileStoreWithCatalog(setup.StateRoot, nativeTemplateValidator, a.catalog, a.config.Geodata.MaxAssetSizeMB, a.config.Xray.AssetDir, a.config.Xray.Binary, a.config.Geodata.UserGeosite, a.config.Geodata.Builder, a.config.Geodata.UserGeoip)
	if err != nil {
		return err
	}
	if err := setup.ConfigureNetwork(setupStore, a.configPath); err != nil {
		return err
	}
	setupStartup, err := setup.NewStartup(ctx, a.config.Xray.Config, setupStore, func(ctx context.Context) error {
		return setup.CheckPendingDatabase(ctx, a.config.Storage.Database)
	})
	if err != nil {
		return err
	}
	a.validator = nativeTemplateValidator
	a.setupStore = setupStore
	a.setupStartup = setupStartup
	return nil
}

// restoreNetwork reloads network values possibly restored by setup recovery.
func (a *application) restoreNetwork() error {
	restored, err := bootstrap.Load(a.configPath)
	if err != nil {
		return err
	}
	a.config.Network = restored.Network
	return nil
}

type setupBundle struct {
	service *setup.Service
	server  *http.Server
	handler http.Handler
	close   func()
}

// buildSetup composes the setup engine and the shared setup handlers. It does
// not open a socket, start a scheduler, reconcile the web service, or handle
// signals.
func (a *application) buildSetup() (*setupBundle, error) {
	services, err := servicecontrol.New(a.configPath)
	if err != nil {
		return nil, err
	}
	discovery := setup.NewDiscoverer()
	engine, err := setup.NewEngine(a.setupStartup.Status(), discovery, a.setupStore, setupXrayStarter{services: services})
	if err != nil {
		return nil, err
	}
	setupService := setup.NewStartupService(a.setupStartup, discovery, engine)
	server := httpapi.NewSetupServer(a.version, setupService, webSettingsResponse(a.config.Web, a.webEnabled))
	expected := xrayservice.Installation{Unit: "xray.service", Binary: a.config.Xray.Binary, Config: a.config.Xray.Config, AssetDir: a.config.Xray.AssetDir}
	server.Handler = xrayservice.Guard(server.Handler, expected, xrayservice.New().Resolve)
	return &setupBundle{
		service: setupService,
		server:  server,
		handler: server.Handler,
		close:   func() {},
	}, nil
}

func (a *application) normalSetupService() *setup.Service {
	return setup.NewStartupService(a.setupStartup, setup.NewDiscoverer(), nil)
}

func webSettingsResponse(config bootstrap.WebConfig, enabled bool) api.WebSettingsResponse {
	return api.WebSettingsResponse{ExporterURL: config.ExporterURL, Enabled: enabled, Host: config.Host, Port: config.Port, ControlEnabled: config.ControlEnabled}
}

// runtimeLifecycle isolates the daemon-only listener and signal boundary so
// offline composition cannot accidentally open a Unix socket or install signal
// handlers. The default is the system implementation.
type runtimeLifecycle struct {
	listen    func(string, string) (net.Listener, error)
	notify    func(context.Context) (context.Context, func())
	parseHHMM func(string) (int, int)
	startJobs func(context.Context, []scheduler.Job, func(string, bool)) func()
}

func systemRuntimeLifecycle() runtimeLifecycle {
	return runtimeLifecycle{
		listen: unixhttp.Listen,
		notify: func(parent context.Context) (context.Context, func()) {
			return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
		},
		parseHHMM: func(value string) (int, int) {
			at, _ := time.Parse("15:04", value)
			return at.Hour(), at.Minute()
		},
		startJobs: scheduler.Start,
	}
}

type setupXrayStarter struct {
	services servicecontrol.Controller
}

func (starter setupXrayStarter) Start(ctx context.Context) error {
	// A previous interrupted setup may have left a process running with the
	// candidate while restoring its boot file. Always load the committed file.
	_, err := starter.services.Execute(ctx, servicecontrol.UnitXray, servicecontrol.ActionRestart)
	return err
}

func (starter setupXrayStarter) Ready(ctx context.Context) error {
	result, err := starter.services.Execute(ctx, servicecontrol.UnitXray, servicecontrol.ActionStatus)
	if err != nil {
		return err
	}
	if result.State != servicecontrol.StateActive {
		return errors.New("xray_service_not_active")
	}
	return nil
}

func (starter setupXrayStarter) Restore(ctx context.Context, existing bool) error {
	action := servicecontrol.ActionStop
	if existing {
		action = servicecontrol.ActionRestart
	}
	_, err := starter.services.Execute(ctx, servicecontrol.UnitXray, action)
	return err
}

func (starter setupXrayStarter) Ensure(ctx context.Context) (bool, error) {
	result, err := starter.services.Execute(ctx, servicecontrol.UnitXray, servicecontrol.ActionStatus)
	if err != nil {
		return false, err
	}
	if result.State == servicecontrol.StateActive {
		return false, nil
	}
	return true, starter.Start(ctx)
}
