package systemsettings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
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
	return geodataSnapshot(raw)
}

func geodataSnapshot(raw []byte) (bootstrap.Config, uint64, error) {
	cfg, err := decodeSettings(raw)
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
	cfg, generation, err := geodataSnapshot(raw)
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
	encoded, err := toml.Marshal(struct {
		Geodata   bootstrap.GeodataConfig   `toml:"geodata"`
		Scheduler bootstrap.SchedulerConfig `toml:"scheduler"`
	}{geo, schedule})
	if err != nil {
		return bootstrap.Config{}, 0, err
	}
	// Parse section boundaries rather than interpreting lines inside TOML
	// multiline strings/arrays as headers. Unrelated text stays byte-for-byte.
	kept, err := withoutGeodataSections(raw)
	if err != nil {
		return bootstrap.Config{}, 0, err
	}
	next := append(bytes.TrimRight(kept, "\n"), []byte("\n\n"+string(encoded))...)

	if _, err := decodeSettings(next); err != nil {
		return bootstrap.Config{}, 0, err
	}
	if err := replace(s.path, next); err != nil {
		return bootstrap.Config{}, 0, err
	}
	s.config.Geodata, s.config.Scheduler = geo, schedule
	return s.readGeodata(ctx)
}

func withoutGeodataSections(raw []byte) ([]byte, error) {
	type segment struct {
		offset int
		drop   bool
	}
	var parser unstable.Parser
	parser.KeepComments = true
	parser.Reset(raw)
	segments := []segment{}
	inTable, owned := false, false
	for parser.NextExpression() {
		node := parser.Expression()
		offset, drop := int(node.Raw.Offset), false
		switch node.Kind {
		case unstable.Table, unstable.ArrayTable, unstable.KeyValue:
			keys := node.Key()
			if !keys.Next() {
				return nil, ErrInvalidSettings
			}
			key := keys.Node()
			offset = int(key.Raw.Offset)
			ownKey := string(key.Data) == "geodata" || string(key.Data) == "scheduler"
			if node.Kind != unstable.KeyValue {
				inTable = true
				owned = ownKey
			}
			drop = owned || (!inTable && ownKey)
		case unstable.Comment:
		default:
			return nil, ErrInvalidSettings
		}
		start := bytes.LastIndexByte(raw[:offset], '\n') + 1
		segments = append(segments, segment{start, drop})
	}
	if parser.Error() != nil {
		return nil, ErrInvalidSettings
	}
	if len(segments) == 0 {
		return append([]byte(nil), raw...), nil
	}
	var kept bytes.Buffer
	kept.Write(raw[:segments[0].offset])
	for i, segment := range segments {
		end := len(raw)
		if i+1 < len(segments) {
			end = segments[i+1].offset
		}
		if !segment.drop {
			kept.Write(raw[segment.offset:end])
		}
	}
	return kept.Bytes(), nil
}
