package setup

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"home-router/xray-core-helper/internal/templateapply"
)

func (s *fileStore) prepareLifecycle(m *setupManifest) error {
	if len(s.lifecycleAssets) > 0 {
		m.LifecycleVersion = 1
	}
	for _, asset := range s.lifecycleAssets {
		raw, _, err := readSafe(asset.Source)
		if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
			return errors.New("native lifecycle source unavailable")
		}
		dir := filepath.Join(s.root, m.ID)
		item := setupFile{ID: asset.ID, Target: asset.Target, Candidate: filepath.Join(dir, string(asset.ID)+".candidate"), Backup: filepath.Join(dir, string(asset.ID)+".backup"), CandidateHash: contentHash(raw), ApplyMode: uint32(asset.Mode), ApplyUID: asset.UID, ApplyGID: asset.GID}
		if err = safePath(item.Target); err != nil {
			return err
		}
		prior, info, err := readSafe(item.Target)
		if err == nil {
			item.Existed = true
			item.Hash = contentHash(prior)
			item.Mode = uint32(info.Mode().Perm())
			st := info.Sys().(*syscall.Stat_t)
			item.UID, item.GID = int(st.Uid), int(st.Gid)
			if err = s.replace(item.Backup, prior, 0600, os.Geteuid(), os.Getegid()); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = s.replace(item.Candidate, raw, 0600, os.Geteuid(), os.Getegid()); err != nil {
			return err
		}
		m.Lifecycle = append(m.Lifecycle, item)
	}
	return nil
}

func (s *fileStore) validateLifecycleManifest(m setupManifest) error {
	// Legacy evidence has no lifecycle snapshots. Do not infer missing files.
	if m.LifecycleVersion == 0 && len(m.Lifecycle) == 0 {
		return nil
	}
	if m.LifecycleVersion != 1 {
		return errors.New("unsupported native lifecycle evidence version")
	}
	if len(m.Lifecycle) != len(s.lifecycleAssets) || len(m.Lifecycle) != 3 {
		return errors.New("incomplete native lifecycle evidence")
	}
	for i, item := range m.Lifecycle {
		asset := s.lifecycleAssets[i]
		dir := filepath.Join(s.root, m.ID)
		if item.ID != asset.ID || !templateapply.LifecycleMember(item.ID) || item.Target != asset.Target || item.Candidate != filepath.Join(dir, string(asset.ID)+".candidate") || item.Backup != filepath.Join(dir, string(asset.ID)+".backup") || item.Preserve || item.ApplyMode != uint32(asset.Mode) || item.ApplyUID != asset.UID || item.ApplyGID != asset.GID || item.Mode & ^uint32(0777) != 0 || item.UID < 0 || item.GID < 0 || len(item.CandidateHash) != 64 || item.Existed && len(item.Hash) != 64 || !item.Existed && (item.Hash != "" || item.Mode != 0 || item.UID != 0 || item.GID != 0) {
			return errors.New("invalid native lifecycle evidence")
		}
		if err := safePath(item.Target); err != nil {
			return err
		}
	}
	return nil
}
