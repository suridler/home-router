// Package systemsettings atomically persists mutable helper daemon settings.
package systemsettings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/pelletier/go-toml/v2"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"

	"home-router/xray-core-helper/internal/atomicfile"
)

var ErrInvalidSettings = errors.New("invalid system settings")

type Service struct {
	mu              sync.RWMutex
	path            string
	config          bootstrap.Config
	loggingMetadata LoggingMetadata
}

func New(path string, config bootstrap.Config) (*Service, error) {
	if !filepath.IsAbs(path) || config.Validate() != nil {
		return nil, ErrInvalidSettings
	}
	return &Service{path: path, config: config}, nil
}

func (service *Service) RetainPerTemplate() int {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.config.Backup.RetainPerTemplate
}

func (service *Service) Get(context.Context) api.SystemSettingsResponse {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return response(service.config)
}

func (service *Service) Put(ctx context.Context, settings api.UpdateSystemSettingsRequest) (api.SystemSettingsResponse, error) {
	if err := ctx.Err(); err != nil {
		return api.SystemSettingsResponse{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if _, err := os.Lstat(service.journalPath()); !errors.Is(err, os.ErrNotExist) {
		return api.SystemSettingsResponse{}, ErrLoggingRecovery
	}
	raw, err := readSettingsFile(service.path, false)
	if err != nil {
		return api.SystemSettingsResponse{}, err
	}
	disk, err := decodeSettings(raw)
	if err != nil || !sameSettings(disk, service.config) {
		return api.SystemSettingsResponse{}, ErrSettingsDrift
	}
	next := service.config
	next.Backup.RetainPerTemplate = settings.Backup.RetainPerTemplate
	if value := settings.Connectivity; value != nil {
		next.Connectivity = bootstrap.ConnectivityConfig{URL: value.URL, ExpectedStatus: value.ExpectedStatus, TimeoutSeconds: value.TimeoutSeconds}
	}
	if err := next.Validate(); err != nil {
		return api.SystemSettingsResponse{}, ErrInvalidSettings
	}
	encoded, err := toml.Marshal(next)
	if err != nil {
		return api.SystemSettingsResponse{}, err
	}
	if err := replace(service.path, encoded); err != nil {
		return api.SystemSettingsResponse{}, err
	}
	service.config = next
	return response(next), nil
}

func response(config bootstrap.Config) api.SystemSettingsResponse {
	return api.SystemSettingsResponse{Backup: api.BackupSettings{RetainPerTemplate: config.Backup.RetainPerTemplate}, Connectivity: api.ConnectivitySettings{URL: config.Connectivity.URL, ExpectedStatus: config.Connectivity.ExpectedStatus, TimeoutSeconds: config.Connectivity.TimeoutSeconds}}
}

func (service *Service) Connectivity() bootstrap.ConnectivityConfig {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.config.Connectivity
}

func replace(path string, contents []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	_, err = atomicfile.Write(path, contents, func(f *os.File) error {
		if err := f.Chmod(info.Mode().Perm()); err != nil {
			return err
		}
		if stat, valid := info.Sys().(*syscall.Stat_t); valid {
			return f.Chown(int(stat.Uid), int(stat.Gid))
		}
		return nil
	})
	return err
}
