// Package readiness provides a bounded, secret-free projection of the
// dependencies required for beta operation.
package readiness

import (
	"context"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/geodatarecovery"
	"home-router/xray-core-helper/internal/transaction"
)

const (
	StatusReady    = "ready"
	StatusDegraded = "degraded"
)

type ConfigStatus interface {
	Status(context.Context) api.StatusResponse
}

type DatabaseStatus interface {
	Metadata(context.Context) (transaction.Metadata, error)
	Pending(context.Context) (*transaction.Pending, error)
}

type XrayAPIStatus interface {
	VerifyActive(context.Context) error
}

type RecoveryStatus interface {
	Status() geodatarecovery.Status
}

type TemplateStatus interface {
	Current() (api.TemplateStatusResponse, bool)
}

type Service struct {
	config    ConfigStatus
	database  DatabaseStatus
	xrayAPI   XrayAPIStatus
	recovery  RecoveryStatus
	templates TemplateStatus
}

func New(config ConfigStatus, database DatabaseStatus, xrayAPI XrayAPIStatus, recovery RecoveryStatus, templates ...TemplateStatus) *Service {
	service := &Service{config: config, database: database, xrayAPI: xrayAPI, recovery: recovery}
	if len(templates) == 1 {
		service.templates = templates[0]
	}
	return service
}

func (service *Service) Check(ctx context.Context) api.ReadinessResponse {
	checks := []api.ReadinessCheck{
		service.checkConfig(ctx),
		service.checkDatabase(ctx),
		service.checkXrayAPI(ctx),
		service.checkRecovery(),
		service.checkTemplates(),
	}
	status := StatusReady
	for _, check := range checks {
		if check.Status != StatusReady {
			status = StatusDegraded
			break
		}
	}
	return api.ReadinessResponse{Status: status, Checks: checks}
}

func (service *Service) checkConfig(ctx context.Context) api.ReadinessCheck {
	if service == nil || service.config == nil || ctx == nil {
		return degradedCheck("config", "config_unavailable")
	}
	status := service.config.Status(ctx)
	switch {
	case status.ConfigDrift || status.ConfigState == "drift":
		return degradedCheck("config", "config_drift")
	case !status.ConfigReadable || !status.ConfigParsed || status.ConfigState != "parsed":
		return degradedCheck("config", "config_unavailable")
	default:
		return readyCheck("config", "config_ready")
	}
}

func (service *Service) checkDatabase(ctx context.Context) api.ReadinessCheck {
	if service == nil || service.database == nil || ctx == nil {
		return degradedCheck("database", "database_unavailable")
	}
	if _, err := service.database.Metadata(ctx); err != nil {
		return degradedCheck("database", "database_unavailable")
	}
	pending, err := service.database.Pending(ctx)
	if err != nil {
		return degradedCheck("database", "database_unavailable")
	}
	if pending != nil {
		return degradedCheck("database", "transaction_pending")
	}
	return readyCheck("database", "database_ready")
}

func (service *Service) checkXrayAPI(ctx context.Context) api.ReadinessCheck {
	if service == nil || service.xrayAPI == nil || ctx == nil {
		return degradedCheck("xray_api", "xray_api_unavailable")
	}
	if err := service.xrayAPI.VerifyActive(ctx); err != nil {
		return degradedCheck("xray_api", "xray_api_unavailable")
	}
	return readyCheck("xray_api", "xray_api_ready")
}

func (service *Service) checkRecovery() api.ReadinessCheck {
	if service == nil || service.recovery == nil {
		return degradedCheck("recovery", "recovery_unavailable")
	}
	status := service.recovery.Status()
	if status.MutationsBlocked || status.Code != geodatarecovery.RecoveryNone.String() {
		return degradedCheck("recovery", "recovery_required")
	}
	return readyCheck("recovery", "recovery_clear")
}

func readyCheck(name, code string) api.ReadinessCheck {
	return api.ReadinessCheck{Name: name, Status: StatusReady, Code: code}
}

func degradedCheck(name, code string) api.ReadinessCheck {
	return api.ReadinessCheck{Name: name, Status: StatusDegraded, Code: code}
}
