package geodatacontract

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/geodataadmin"
)

type Contract struct {
	MaxAssetSizeMB int
	Service        *geodataadmin.Service
	Executor       *geodataadmin.Executor
}

func (contract Contract) GetSettings(ctx context.Context) (api.VersionedGeodataSettings, error) {
	if contract.Service == nil {
		return api.VersionedGeodataSettings{}, errors.New("geodata service unavailable")
	}
	settings, err := contract.Service.Settings(ctx)
	if err != nil {
		return api.VersionedGeodataSettings{}, err
	}
	return api.VersionedGeodataSettings{
		Generation: settings.Generation, Settings: contractAPIGeodataSettings(settings.Settings),
	}, nil
}

func (contract Contract) GetStatus(ctx context.Context) (api.GeodataStatusResponse, error) {
	if contract.Service == nil {
		return api.GeodataStatusResponse{}, errors.New("geodata service unavailable")
	}
	status, err := contract.Service.Status(ctx)
	if err != nil {
		return api.GeodataStatusResponse{}, err
	}
	var operation *api.GeodataOperation
	if status.Operation != nil {
		projected := contractAPIGeodataOperation(*status.Operation)
		operation = &projected
	}
	return api.GeodataStatusResponse{
		MaxAssetSizeMB: status.Settings.Settings.MaxAssetSizeMB,
		Settings: api.VersionedGeodataSettings{
			Generation: status.Settings.Generation,
			Settings:   contractAPIGeodataSettings(status.Settings.Settings),
		},
		LastSuccessfulAt: status.LastSuccessfulAt, NextScheduledAt: status.NextScheduledAt,
		Operation: operation,
		Recovery: api.GeodataRecovery{
			Phase: status.Recovery.Phase, Code: status.Recovery.Code,
			MutationsBlocked: status.Recovery.MutationsBlocked,
		},
	}, nil
}

func (contract Contract) PutSettings(ctx context.Context, generation uint64, settings api.GeodataSettings) (api.VersionedGeodataSettings, error) {
	if contract.Service == nil {
		return api.VersionedGeodataSettings{}, errors.New("geodata service unavailable")
	}
	updated, err := contract.Service.ReplaceSettings(ctx, generation, contractAdminSettings(settings))
	switch {
	case errors.Is(err, geodataadmin.ErrRecoveryRequired):
		return api.VersionedGeodataSettings{}, &api.ContractError{
			Code: "geodata_recovery_required", HTTPStatus: http.StatusServiceUnavailable,
		}
	case errors.Is(err, geodataadmin.ErrGenerationConflict):
		return api.VersionedGeodataSettings{}, &api.ContractError{
			Code: "generation_conflict", HTTPStatus: http.StatusConflict,
		}
	case errors.Is(err, geodataadmin.ErrInvalidSettings):
		return api.VersionedGeodataSettings{}, &api.ContractError{
			Code: "invalid_geodata_settings", HTTPStatus: http.StatusBadRequest,
		}
	case err != nil:
		return api.VersionedGeodataSettings{}, err
	default:
		return api.VersionedGeodataSettings{
			Generation: updated.Generation, Settings: contractAPIGeodataSettings(updated.Settings),
		}, nil
	}
}

func (contract Contract) Execute(ctx context.Context, key string) (api.GeodataOperation, error) {
	if contract.Executor == nil {
		return api.GeodataOperation{}, &api.ContractError{
			Code: "geodata_update_unavailable", HTTPStatus: http.StatusServiceUnavailable,
		}
	}
	operation, err := contract.Executor.Execute(ctx, key)
	if err != nil {
		var executionError *geodataadmin.ExecutionError
		if errors.As(err, &executionError) {
			return contractAPIGeodataOperation(operation), &api.ContractError{
				Message: executionError.Message,
				Code:    executionError.Code, HTTPStatus: executionError.HTTPStatus, MaxAssetSizeMB: executionError.MaxAssetSizeMB,
			}
		}
		return api.GeodataOperation{}, fmt.Errorf("execute geodata operation: %w", err)
	}
	return contractAPIGeodataOperation(operation), nil
}

func contractAdminSettings(settings api.GeodataSettings) geodataadmin.Settings {
	return geodataadmin.Settings{
		MaxAssetSizeMB: settings.MaxAssetSizeMB, Builder: settings.Builder, UserGeosite: settings.UserGeosite, UserGeoip: settings.UserGeoip, Schedule: settings.Schedule,
		Origins: settings.Origins,
		Geosite: geodataadmin.SourceSettings{
			URL: settings.Geosite.URL,
			Checksum: geodataadmin.ChecksumSettings{
				URL: settings.Geosite.Checksum.URL, Algorithm: settings.Geosite.Checksum.Algorithm,
			},
		},
		Geoip: geodataadmin.SourceSettings{
			URL: settings.Geoip.URL,
			Checksum: geodataadmin.ChecksumSettings{
				URL: settings.Geoip.Checksum.URL, Algorithm: settings.Geoip.Checksum.Algorithm,
			},
		},
	}
}

func contractAPIGeodataSettings(settings geodataadmin.Settings) api.GeodataSettings {
	return api.GeodataSettings{
		MaxAssetSizeMB: settings.MaxAssetSizeMB, Builder: settings.Builder, UserGeosite: settings.UserGeosite, UserGeoip: settings.UserGeoip, Schedule: settings.Schedule,
		Origins: settings.Origins,
		Geosite: api.GeodataSourceSettings{
			URL: settings.Geosite.URL,
			Checksum: api.GeodataChecksumSettings{
				URL: settings.Geosite.Checksum.URL, Algorithm: settings.Geosite.Checksum.Algorithm,
			},
		},
		Geoip: api.GeodataSourceSettings{
			URL: settings.Geoip.URL,
			Checksum: api.GeodataChecksumSettings{
				URL: settings.Geoip.Checksum.URL, Algorithm: settings.Geoip.Checksum.Algorithm,
			},
		},
	}
}

func contractAPIGeodataOperation(operation geodataadmin.Operation) api.GeodataOperation {
	return api.GeodataOperation{
		Report: operation.Report,
		ID:     operation.ID, SettingsGeneration: operation.SettingsGeneration,
		State: string(operation.State), ResultCode: operation.ResultCode,
		ErrorCode: operation.ErrorCode, CreatedAt: operation.CreatedAt,
		UpdatedAt: operation.UpdatedAt, FinishedAt: operation.FinishedAt,
	}
}
