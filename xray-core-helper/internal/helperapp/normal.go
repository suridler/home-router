package helperapp

import (
	"context"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"sync"

	"home-router/xray-core-helper/internal/acceptsystem"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/candidatevalidation"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/domainservice"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/errorhistory"
	"home-router/xray-core-helper/internal/geodataadmin"
	"home-router/xray-core-helper/internal/geodatacontract"
	"home-router/xray-core-helper/internal/geodatadownload"
	"home-router/xray-core-helper/internal/geodatarecovery"
	"home-router/xray-core-helper/internal/geodataupdate"
	"home-router/xray-core-helper/internal/httpapi"
	"home-router/xray-core-helper/internal/logfileprep"
	"home-router/xray-core-helper/internal/loggingservice"
	"home-router/xray-core-helper/internal/logpathcheck"
	"home-router/xray-core-helper/internal/logreader"
	"home-router/xray-core-helper/internal/logrotation"
	"home-router/xray-core-helper/internal/nativerender"
	"home-router/xray-core-helper/internal/networkprobe"
	"home-router/xray-core-helper/internal/readiness"
	"home-router/xray-core-helper/internal/routerdiagnostics"
	"home-router/xray-core-helper/internal/routingservice"
	"home-router/xray-core-helper/internal/safeapply"
	"home-router/xray-core-helper/internal/servicecontrol"
	"home-router/xray-core-helper/internal/setup"
	"home-router/xray-core-helper/internal/systemsettings"
	"home-router/xray-core-helper/internal/templateapply"
	"home-router/xray-core-helper/internal/templatebackup"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templateprojection"
	"home-router/xray-core-helper/internal/templatestatus"
	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/xrayconfig"
	"home-router/xray-core-helper/internal/xrayservice"
	"home-router/xray-core-helper/internal/xrayvalidation"
)

// normalBundle owns the complete normal-mode service graph. handler is the
// guarded HTTP handler; server is used only by the daemon lifecycle.
type normalBundle struct {
	handler      http.Handler
	server       *http.Server
	logMutations *loggingservice.Service
	setupState   *setup.Service
	close        func()
}

// Close releases every store opened by buildNormal. It is safe to call more
// than once.
func (bundle *normalBundle) Close() {
	if bundle != nil && bundle.close != nil {
		bundle.close()
	}
}

type closeStack struct {
	mu   sync.Mutex
	once sync.Once
	fns  []func() error
}

func (stack *closeStack) add(fn func() error) {
	if fn == nil {
		return
	}
	stack.mu.Lock()
	defer stack.mu.Unlock()
	stack.fns = append(stack.fns, fn)
}

func (stack *closeStack) close() {
	stack.once.Do(func() {
		stack.mu.Lock()
		fns := append([]func() error(nil), stack.fns...)
		stack.fns = nil
		stack.mu.Unlock()
		for index := len(fns) - 1; index >= 0; index-- {
			_ = fns[index]()
		}
	})
}

// buildNormal composes all normal-mode services and typed handlers. It does not
// open a socket, start background jobs, reconcile the web service, or handle
// signals; the caller owns those lifecycle decisions.
func (a *application) buildNormal(ctx context.Context, setupState *setup.Service) (bundle *normalBundle, resultErr error) {
	cleanup := &closeStack{}
	defer func() {
		if resultErr != nil {
			cleanup.close()
		}
	}()

	settingsService, err := systemsettings.New(a.configPath, a.config)
	if err != nil {
		return nil, err
	}
	store, err := transaction.OpenStore(ctx, a.config.Storage.Database)
	if err != nil {
		return nil, err
	}
	cleanup.add(store.Close)
	validator, err := transaction.NewXrayValidator(a.config.Xray.Binary, a.config.Xray.AssetDir)
	if err != nil {
		return nil, err
	}
	identityResolver := logfileprep.NewSystemIdentityResolver()
	if _, err := identityResolver.ResolveXrayIdentity(ctx); err != nil {
		return nil, errors.New("Xray service identity is unavailable")
	}
	serviceController, err := servicecontrol.New(a.configPath)
	if err != nil {
		return nil, err
	}
	restarter, err := servicecontrol.NewXrayRestarter(serviceController)
	if err != nil {
		return nil, err
	}
	engine, err := transaction.NewEngine(a.config.Xray.Config, store, validator, restarter)
	if err != nil {
		return nil, err
	}
	logPreparer, err := logfileprep.New(identityResolver, nil)
	if err != nil {
		return nil, err
	}
	logRotationService, err := logrotation.New()
	if err != nil {
		return nil, err
	}
	logMutationService, err := loggingservice.New(engine, logPreparer, logRotationService, a.config.Logging)
	if err != nil {
		return nil, err
	}
	if err := logMutationService.SetPathPolicy(logpathcheck.New()); err != nil {
		return nil, err
	}
	settingsService.SetLoggingMetadata(store)
	if err := logMutationService.SetSettings(settingsService, func(values bootstrap.LoggingConfig) (loggingservice.FilePreparer, error) {
		access, errorPath := values.AccessPath, values.ErrorPath
		if access == "" || access == "none" {
			access = values.AccessFilePath
		}
		if errorPath == "" || errorPath == "none" {
			errorPath = values.ErrorFilePath
		}
		return logfileprep.NewWithPaths(identityResolver, nil, access, errorPath)
	}); err != nil {
		return nil, err
	}
	if err := engine.Initialize(ctx); err != nil {
		return nil, err
	}
	if err := engine.Recover(ctx); err != nil {
		return nil, err
	}
	if err := settingsService.RecoverLogging(ctx); err != nil {
		return nil, err
	}
	config := a.config
	config.Logging = settingsService.Logging()

	reader := xrayconfig.NewReader(xrayconfig.Settings{
		Binary:   config.Xray.Binary,
		Config:   config.Xray.Config,
		API:      config.Xray.API,
		AssetDir: config.Xray.AssetDir,
	}, store)
	domainsStore, err := domainstore.Open(ctx, config.Storage.Database)
	if err != nil {
		return nil, err
	}
	cleanup.add(domainsStore.Close)
	var recoveryProvider geodataadmin.RecoveryProvider = cleanGeodataRecovery{}
	geodataStore, err := geodataadmin.Open(ctx, config.Storage.Database, config.Geodata)
	if err != nil {
		return nil, err
	}
	cleanup.add(geodataStore.Close)
	geodataService, err := geodataadmin.NewService(geodataadmin.ServiceOptions{
		Store: geodataStore, Settings: settingsService, Recovery: recoveryProvider,
	})
	if err != nil {
		return nil, err
	}
	routingClient, err := routingservice.NewClient(config.Xray.Binary, config.Xray.API, nil)
	if err != nil {
		return nil, err
	}
	domains, err := domainservice.New(domainsStore, reader, routingClient)
	if err != nil {
		return nil, err
	}
	domainReconciler, err := domainservice.NewRuntimeReconciler(domains, xrayservice.New().RuntimeIdentity)
	if err != nil {
		return nil, err
	}
	domainReconciler.BeforeApply = func(ctx context.Context, token string) error {
		assets, err := reader.UserlistRoutingAssets(ctx)
		if err != nil {
			return err
		}
		return domainsStore.ActivateBaselines(ctx, token, config.Xray.AssetDir, assets)
	}
	geodataExecutor, err := newGeodataExecutor(config, geodataService, domainsStore, domains,
		serviceController, routingClient)
	if err != nil {
		return nil, err
	}
	cleanup.add(geodataExecutor.Close)
	geodataContract := geodatacontract.Contract{MaxAssetSizeMB: config.Geodata.MaxAssetSizeMB, Service: geodataService, Executor: geodataExecutor}
	readinessHealth, err := geodataupdate.NewRoutingServiceHealthVerifier(routingClient)
	if err != nil {
		return nil, err
	}
	templateStatusService, err := templatestatus.ProductionWithValidator(a.catalog, a.validator, config.Geodata.UserGeosite)
	if err != nil {
		return nil, err
	}
	xrayValues := xrayconfig.TemplateValues{Network: config.Network, Logging: config.Logging, API: config.Xray.API, UserGeosite: config.Geodata.UserGeosite, UserGeoip: config.Geodata.UserGeoip}
	nativeRenderer := nativerender.Renderer{Network: config.Network, Catalog: a.catalog, Xray: &xrayValues, Logging: settingsService.Logging}
	renderTemplate := func(ctx context.Context, entry templatecatalog.Entry, raw []byte) ([]byte, error) {
		if entry.ID == templatecatalog.XrayConfig {
			return nativeRenderer.RenderXray(ctx, raw)
		}
		return nativeRenderer.Render(ctx, raw)
	}
	templateStatusService.SetNativeRenderer(renderTemplate)
	templateProjection, err := templateprojection.New(templateStatusService)
	if err != nil {
		return nil, err
	}
	templateProjection.Initialize(ctx)
	backupStore, err := templatebackup.NewWithCatalog(settingsService.RetainPerTemplate, func(ctx context.Context, id templatecatalog.ID, path string) error {
		return nativeRenderer.ValidateStored(ctx, id, path, a.validator)
	}, a.catalog)
	if err != nil {
		return nil, err
	}
	acceptSystemEngine, err := acceptsystem.NewWithCatalog(templateProjection, a.validator, backupStore, a.catalog)
	if err != nil {
		return nil, err
	}
	acceptSystemEngine.SetNativeRenderer(renderTemplate)
	templateCrossChecker, err := templateapply.NewCrossCheckerWithCatalog(a.validator, a.catalog, config.Geodata.UserGeosite)
	if err != nil {
		return nil, err
	}
	templateRuntime, err := templateapply.NewCommandRuntimeWithXray(config.Xray.Binary, a.configPath)
	if err != nil {
		return nil, err
	}
	templateApplyEngine, err := templateapply.NewEngineWithCatalog(templateCrossChecker, backupStore, templateRuntime, a.catalog, config.Geodata.UserGeosite)
	if err != nil {
		return nil, err
	}
	templateCrossChecker.SetNetwork(config.Network)
	templateApplyEngine.SetNetwork(config.Network)
	templateCrossChecker.SetXrayValues(xrayValues)
	templateApplyEngine.SetXrayValues(xrayValues)
	templateApplyEngine.SetLoggingValues(settingsService.Logging)
	templateCrossChecker.SetLoggingValues(settingsService.Logging)
	safeApplyStore, err := safeapply.Open(ctx, config.Storage.Database)
	if err != nil {
		return nil, err
	}
	cleanup.add(safeApplyStore.Close)
	safeApplyTimer, err := safeapply.NewSystemdTimer()
	if err != nil {
		return nil, err
	}
	safeApplyService, err := safeapply.NewService(safeApplyStore, templateApplyEngine, safeApplyTimer, settingsService.RetainPerTemplate)
	if err != nil {
		return nil, err
	}
	if _, err = safeApplyService.Recover(ctx); err != nil {
		if pending, readErr := safeApplyService.Pending(ctx); readErr != nil || pending == nil {
			return nil, err
		}
		// Keep read/status and explicit recovery available. The global pending
		// gate rejects mutations; never log secret-bearing native errors.
		log.Print("safe-apply recovery incomplete; inspect safe-apply status and retry recovery; configuration writes blocked")
	}
	readinessService := readiness.New(reader, store, readinessHealth, recoveryProvider, templateProjection)
	validationService, err := xrayvalidation.New(config, nil)
	if err != nil {
		return nil, err
	}
	diagnosticsService, err := routerdiagnostics.NewWithXrayPath(config.Xray.Config, config.Network)
	if err != nil {
		return nil, err
	}
	logReadService := logreader.New(reader)
	errorHistory, err := errorhistory.Open(ctx, config.Storage.Database, logReadService)
	if err != nil {
		return nil, err
	}
	cleanup.add(errorHistory.Close)
	webSettings := webSettingsResponse(config.Web, a.webEnabled)
	networkProbe, err := networkprobe.New(filepath.Join(filepath.Dir(config.Storage.Database), "network-probes.json"), settingsService.Connectivity)
	if err != nil {
		return nil, err
	}
	server := httpapi.NewServer(a.version, reader, engine, domains, httpapi.ServerServices{
		NetworkProbe:    networkProbe,
		DomainReconcile: domainReconciler,
		Web:             &webSettings,
		Setup:           setupState,
		Admin:           geodataContract, Executor: geodataContract,
		Validation: validationService, Diagnostics: diagnosticsService,
		Connectivity: routerdiagnostics.NewConnectivity(readinessHealth.VerifyActive, reader.OutboundEndpoint),
		Services:     serviceController, Logs: logReadService, Errors: errorHistory,
		Logging: logMutationService, Readiness: readinessService, Templates: templateProjection, Settings: settingsService, Backups: backupStore, AcceptSystem: acceptSystemEngine, ApplyTemplate: safeApplyService,
	})
	server.Handler = xrayservice.Guard(server.Handler, a.installation, xrayservice.New().Resolve, func(r *http.Request) bool {
		return httpapi.IndependentNativeOperation(r, safeApplyStore.Transaction)
	})
	return &normalBundle{
		handler:      server.Handler,
		server:       server,
		logMutations: logMutationService,
		setupState:   setupState,
		close:        cleanup.close,
	}, nil
}

func newGeodataExecutor(
	config bootstrap.Config,
	admin *geodataadmin.Service,
	domainsStore *domainstore.Store,
	overlay geodataupdate.RuntimeOverlay,
	services geodataupdate.ServiceController,
	routingHealth geodataupdate.RoutingHealth,
) (*geodataadmin.Executor, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	if domainsStore == nil || overlay == nil || services == nil {
		return nil, errors.New("geodata update dependencies are required")
	}
	restarter, err := geodataupdate.NewServiceControlRestarter(services)
	if err != nil {
		return nil, err
	}
	updater, err := geodataupdate.NewCycle(geodataupdate.CycleOptions{
		AssetDir: config.Xray.AssetDir, Store: domainsStore,
		RoutingAssets: xrayconfig.NewReader(xrayconfig.Settings{Config: config.Xray.Config}).UserlistRoutingAssets,
		Fetch: func(ctx context.Context, cfg bootstrap.GeodataConfig, name string) ([]byte, error) {
			return geodatadownload.New(geodatadownload.Options{MaxAssetSizeMB: cfg.MaxAssetSizeMB}).FetchAsset(ctx, cfg, name)
		},
		Validate: func(ctx context.Context, cfg bootstrap.GeodataConfig, paths map[string]string) error {
			current := config
			current.Geodata = cfg
			validator, err := candidatevalidation.New(current, candidatevalidation.Options{})
			if err != nil {
				return err
			}
			return validator.ValidateCandidates(ctx, paths)
		},
		Restart: restarter.RestartWhileLocked, Overlay: overlay.ApplyWhileLocked,
		Identity: xrayservice.New().RuntimeIdentity,
	})
	if err != nil {
		return nil, err
	}
	return geodataadmin.NewExecutor(admin, config.Geodata, updater)
}

func geodataSourcesEnabled(config bootstrap.GeodataConfig) bool {
	return len(config.Origins) > 0
}

type unavailableGeodataRecovery struct{}

func (unavailableGeodataRecovery) Status() geodatarecovery.Status {
	return geodatarecovery.UnavailableStatus()
}

type liveGeodataRecovery struct {
	manager geodataadmin.RecoveryProvider
}

func (recovery liveGeodataRecovery) Status() geodatarecovery.Status {
	status := recovery.manager.Status()
	if coordinator.RecoveryBlocked() && !status.MutationsBlocked {
		return geodatarecovery.UnavailableStatus()
	}
	return status
}

type cleanGeodataRecovery struct{}

func (cleanGeodataRecovery) Status() geodatarecovery.Status {
	return geodatarecovery.Status{Code: geodatarecovery.RecoveryNone.String(), Result: geodatarecovery.Result{State: geodatarecovery.State{Phase: geodatarecovery.PhaseClean}, Plan: geodatarecovery.Plan{Phase: geodatarecovery.PhaseClean}}}
}
