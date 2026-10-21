package systemsettings

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"

	"github.com/pelletier/go-toml/v2"
	"home-router/xray-core-helper/internal/bootstrap"
)

// Geodata reads the file again: a manual edit takes effect on the next cycle.
// Generation identifies the whole settings snapshot, including the schedule.
func (s *Service) Geodata(ctx context.Context) (bootstrap.Config, uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readGeodata(ctx)
}

func (s *Service) readGeodata(ctx context.Context) (bootstrap.Config, uint64, error) {
	if err := ctx.Err(); err != nil {
		return bootstrap.Config{}, 0, err
	}
	raw, err := readSettingsFile(s.path, false)
	if err != nil {
		return bootstrap.Config{}, 0, err
	}
	return s.geodataSnapshot(raw)
}

func (s *Service) geodataSnapshot(raw []byte) (bootstrap.Config, uint64, error) {
	cfg, err := s.decodeSettings(raw)
	if err != nil {
		return bootstrap.Config{}, 0, err
	}
	encoded, err := toml.Marshal(struct {
		Geodata   bootstrap.GeodataConfig
		Scheduler bootstrap.SchedulerConfig
	}{cfg.Geodata, cfg.Scheduler})
	if err != nil {
		return bootstrap.Config{}, 0, err
	}
	sum := sha256.Sum256(encoded)
	// 48 bits remain exact in JavaScript numbers and fit SQLite INTEGER.
	generation := binary.BigEndian.Uint64(sum[:8]) >> 16
	return cfg, generation, nil
}

func (s *Service) PutGeodata(ctx context.Context, expected uint64, geo bootstrap.GeodataConfig, schedule bootstrap.SchedulerConfig) (bootstrap.Config, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return bootstrap.Config{}, 0, err
	}
	raw, err := readSettingsFile(s.path, false)
	if err != nil {
		return bootstrap.Config{}, 0, err
	}
	cfg, generation, err := s.geodataSnapshot(raw)
	if err != nil {
		return bootstrap.Config{}, 0, err
	}
	if generation != expected {
		return bootstrap.Config{}, 0, ErrSettingsDrift
	}
	cfg.Geodata, cfg.Scheduler = geo, schedule
	if err := cfg.Validate(); err != nil {
		return bootstrap.Config{}, 0, ErrInvalidSettings
	}
	if _, err := os.Lstat(s.journalPath()); !errors.Is(err, os.ErrNotExist) {
		return bootstrap.Config{}, 0, ErrLoggingRecovery
	}
	next, err := bootstrap.UpdateOverrides(raw, cfg, "geodata", "scheduler")
	if err != nil {
		return bootstrap.Config{}, 0, err
	}

	if err := replace(s.path, next); err != nil {
		return bootstrap.Config{}, 0, err
	}
	s.config.Geodata, s.config.Scheduler = geo, schedule
	return s.readGeodata(ctx)
}
