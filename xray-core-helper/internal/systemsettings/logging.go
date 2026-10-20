package systemsettings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/pelletier/go-toml/v2"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/transaction"
)

var ErrLoggingRecovery = errors.New("helper logging settings require recovery")
var ErrSettingsDrift = errors.New("helper settings changed outside the daemon")

const maxSettingsBytes = 1 << 20

// Configuration state, not a second Xray config. The journal contains only the
// two helper TOML snapshots and the corresponding committed Xray fingerprints.
type loggingJournal struct {
	Version  int                  `json:"version"`
	Before   transaction.Metadata `json:"before"`
	After    transaction.Metadata `json:"after"`
	Previous []byte               `json:"previous"`
	Next     []byte               `json:"next"`
}

type LoggingMetadata interface {
	Metadata(context.Context) (transaction.Metadata, error)
	Pending(context.Context) (*transaction.Pending, error)
}

func (s *Service) Logging() bootstrap.LoggingConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.Logging
}

// SetLoggingMetadata is wired once, before serving requests or recovery.
func (s *Service) SetLoggingMetadata(metadata LoggingMetadata) { s.loggingMetadata = metadata }

func (s *Service) journalPath() string {
	return filepath.Join(filepath.Dir(s.path), "."+filepath.Base(s.path)+".logging-pending")
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func readSettingsFile(path string, private bool) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4*maxSettingsBytes {
		return nil, ErrLoggingRecovery
	}
	if private {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != 0600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return nil, ErrLoggingRecovery
		}
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4*maxSettingsBytes+1))
	if err != nil || len(raw) > 4*maxSettingsBytes {
		return nil, ErrLoggingRecovery
	}
	return raw, nil
}

func decodeSettings(raw []byte) (bootstrap.Config, error) {
	c := bootstrap.Defaults()
	if len(raw) > maxSettingsBytes {
		return bootstrap.Config{}, ErrInvalidSettings
	}
	decoder := toml.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return bootstrap.Config{}, ErrInvalidSettings
	}
	if c.Validate() != nil {
		return bootstrap.Config{}, ErrInvalidSettings
	}
	return c, nil
}

// Compare serialized settings: TOML encodes nil/empty slices identically and
// deliberately excludes service-discovered paths.
func sameSettings(a, b bootstrap.Config) bool {
	first, err := toml.Marshal(a)
	second, otherErr := toml.Marshal(b)
	return err == nil && otherErr == nil && bytes.Equal(first, second)
}

// PrepareLogging runs under the config mutation coordinator. The service lock
// also prevents an unrelated settings PUT from overwriting the prepared TOML.
func (s *Service) PrepareLogging(ctx context.Context, next bootstrap.LoggingConfig, before, after transaction.Metadata) (transaction.MutationPreparation, error) {
	if ctx == nil || ctx.Err() != nil || next.Validate() != nil || s.loggingMetadata == nil {
		return nil, ErrInvalidSettings
	}
	s.mu.Lock()
	unlock := true
	defer func() {
		if unlock {
			s.mu.Unlock()
		}
	}()
	if _, err := os.Lstat(s.journalPath()); !errors.Is(err, os.ErrNotExist) {
		return nil, ErrLoggingRecovery
	}
	raw, err := readSettingsFile(s.path, false)
	if err != nil {
		return nil, err
	}
	old, err := decodeSettings(raw)
	if err != nil || !sameSettings(old, s.config) {
		return nil, ErrSettingsDrift
	}
	newConfig := old
	newConfig.Logging = next
	encoded, err := toml.Marshal(newConfig)
	if err != nil {
		return nil, err
	}
	j := loggingJournal{Version: 1, Before: before, After: after, Previous: raw, Next: encoded}
	if validateLoggingJournal(j) != nil {
		return nil, ErrLoggingRecovery
	}
	current, err := s.loggingMetadata.Metadata(ctx)
	if err != nil || current != before {
		return nil, ErrLoggingRecovery
	}
	pending, err := s.loggingMetadata.Pending(ctx)
	if err != nil || pending != nil {
		return nil, ErrLoggingRecovery
	}
	if err := s.writeLoggingJournal(j); err != nil {
		return nil, errors.Join(ErrLoggingRecovery, err)
	}
	snapshot := &loggingPreparation{service: s, journal: j}
	// Even a failed directory fsync after rename must resolve from the journal.
	if err := replace(s.path, encoded); err != nil {
		if rollbackErr := snapshot.finish(false); rollbackErr != nil {
			return nil, ErrLoggingRecovery
		}
		return nil, err
	}
	unlock = false
	return snapshot, nil
}

type loggingPreparation struct {
	service *Service
	journal loggingJournal
	done    bool
}

func (p *loggingPreparation) Commit() error   { return p.close(true) }
func (p *loggingPreparation) Rollback() error { return p.close(false) }
func (p *loggingPreparation) close(commit bool) error {
	if p.done {
		return nil
	}
	defer p.service.mu.Unlock()
	p.done = true
	return p.finish(commit)
}

func (p *loggingPreparation) finish(commit bool) error {
	s := p.service
	// A pending Xray rollback must complete before deciding which TOML to keep.
	pending, err := s.loggingMetadata.Pending(context.Background())
	if err != nil || pending != nil {
		return ErrLoggingRecovery
	}
	state, err := s.loggingMetadata.Metadata(context.Background())
	if err != nil {
		return ErrLoggingRecovery
	}
	switch {
	case state == p.journal.After && (commit || p.journal.Before != p.journal.After):
		commit = true
	case state == p.journal.Before:
		commit = false
	default:
		return ErrLoggingRecovery
	}
	desired := p.journal.Previous
	if commit {
		desired = p.journal.Next
	}
	if err := s.resolveLogging(p.journal, desired); err != nil {
		return err
	}
	return nil
}

func validateLoggingJournal(j loggingJournal) error {
	validHash := func(h string) bool {
		b, e := hex.DecodeString(h)
		return e == nil && len(b) == sha256.Size && hex.EncodeToString(b) == h
	}
	if j.Version != 1 || !validHash(j.Before.ContentHash) || !validHash(j.After.ContentHash) {
		return ErrLoggingRecovery
	}
	if j.Before.ContentHash == j.After.ContentHash {
		if j.Before.Generation != j.After.Generation {
			return ErrLoggingRecovery
		}
	} else if j.After.Generation == 0 || j.After.Generation != j.Before.Generation+1 {
		return ErrLoggingRecovery
	}
	before, err := decodeSettings(j.Previous)
	after, afterErr := decodeSettings(j.Next)
	if err != nil || afterErr != nil {
		return ErrLoggingRecovery
	}
	before.Logging = after.Logging
	if !sameSettings(before, after) {
		return ErrLoggingRecovery
	}
	return nil
}

func (s *Service) writeLoggingJournal(j loggingJournal) error {
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.journalPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Chmod(0600)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return syncSettingsDirectory(s.path)
}

func syncSettingsDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Service) resolveLogging(j loggingJournal, desired []byte) error {
	current, err := readSettingsFile(s.path, false)
	if err != nil || !bytes.Equal(current, j.Previous) && !bytes.Equal(current, j.Next) {
		return ErrSettingsDrift
	}
	if !bytes.Equal(current, desired) {
		if err := replace(s.path, desired); err != nil {
			return ErrLoggingRecovery
		}
	}
	config, err := decodeSettings(desired)
	if err != nil {
		return ErrLoggingRecovery
	}
	// Preserve service-discovered fields, which are intentionally absent in TOML.
	config.Xray.Binary = s.config.Xray.Binary
	config.Xray.Config = s.config.Xray.Config
	config.Xray.AssetDir = s.config.Xray.AssetDir
	s.config = config
	if err := os.Remove(s.journalPath()); err != nil {
		return ErrLoggingRecovery
	}
	return syncSettingsDirectory(s.path)
}

// RecoverLogging runs after Xray transaction recovery and before API admission.
func (s *Service) RecoverLogging(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil || s.loggingMetadata == nil {
		return ErrLoggingRecovery
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := readSettingsFile(s.journalPath(), true)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrLoggingRecovery
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var j loggingJournal
	if decoder.Decode(&j) != nil || validateLoggingJournal(j) != nil {
		return ErrLoggingRecovery
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrLoggingRecovery
	}
	pending, err := s.loggingMetadata.Pending(ctx)
	if err != nil || pending != nil {
		return ErrLoggingRecovery
	}
	state, err := s.loggingMetadata.Metadata(ctx)
	if err != nil {
		return ErrLoggingRecovery
	}
	switch state {
	case j.After:
		return s.resolveLogging(j, j.Next)
	case j.Before:
		return s.resolveLogging(j, j.Previous)
	default:
		return ErrLoggingRecovery
	}
}
