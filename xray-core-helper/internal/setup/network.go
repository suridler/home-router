package setup

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/pelletier/go-toml/v2"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/templatecatalog"
)

const networkID templatecatalog.ID = "helper-network"

// ConfigureNetwork binds setup evidence to the daemon's startup config path.
// The path is never supplied by a setup HTTP request.
func ConfigureNetwork(store SetupStore, path string) error {
	s, ok := store.(*fileStore)
	if !ok || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("invalid setup config path")
	}
	c, err := bootstrap.Load(path)
	if err != nil {
		return err
	}
	s.configPath, s.network, s.userGeoip = path, c.Network, c.Geodata.UserGeoip
	return nil
}

func (m setupManifest) files() []setupFile {
	items := append([]setupFile(nil), m.Items...)
	items = append(items, m.Lifecycle...)
	if m.Network != nil {
		items = append(items, *m.Network)
	}
	return items
}

func (s *fileStore) prepareNetwork(m *setupManifest, plan Plan) error {
	if s.configPath == "" || plan.Gateway == nil {
		return nil
	}
	prior, info, err := readSafe(s.configPath)
	if err != nil {
		return err
	}
	c, err := bootstrap.Load(s.configPath)
	if err != nil {
		return err
	}
	// A changed configuration requires a fresh plan; never overwrite unrelated edits.
	if c.Network != s.network {
		return errors.New("setup network settings changed")
	}
	c.Network = plan.network
	if err := c.Validate(); err != nil {
		return err
	}
	encoded, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	dir := filepath.Join(s.root, m.ID)
	item := setupFile{ID: networkID, Target: s.configPath, Candidate: filepath.Join(dir, "helper-network.candidate.toml"), Backup: filepath.Join(dir, "helper-network.backup"), Existed: true, Hash: contentHash(prior), CandidateHash: contentHash(encoded), Mode: uint32(info.Mode().Perm()), UID: int(st.Uid), GID: int(st.Gid), ApplyMode: uint32(info.Mode().Perm()), ApplyUID: int(st.Uid), ApplyGID: int(st.Gid)}
	if err := s.replace(item.Backup, prior, 0600, os.Geteuid(), os.Getegid()); err != nil {
		return err
	}
	if err := s.replace(item.Candidate, encoded, 0600, os.Geteuid(), os.Getegid()); err != nil {
		return err
	}
	m.Network = &item
	if m.Version < 4 {
		m.Version = 4
	}
	return nil
}

func (s *fileStore) validateNetworkManifest(m setupManifest) error {
	if m.Network == nil {
		if m.Version == 4 || m.Version == 5 && s.configPath != "" {
			return errors.New("missing setup network evidence")
		}
		return nil
	}
	i := m.Network
	dir := filepath.Join(s.root, m.ID)
	if (m.Version != 4 && m.Version != 5) || s.configPath == "" || i.Target != s.configPath || i.ID != networkID || i.Candidate != filepath.Join(dir, "helper-network.candidate.toml") || i.Backup != filepath.Join(dir, "helper-network.backup") || !i.Existed || i.Preserve || len(i.Hash) != 64 || len(i.CandidateHash) != 64 || i.Mode == 0 || i.Mode & ^uint32(0777) != 0 || i.ApplyMode != i.Mode || i.UID < 0 || i.GID < 0 || i.ApplyUID != i.UID || i.ApplyGID != i.GID {
		return errors.New("invalid setup network evidence")
	}
	return safePath(i.Target)
}

func (s *fileStore) manifestNetwork(m setupManifest) (bootstrap.NetworkConfig, error) {
	if m.Network == nil {
		return s.network, nil
	}
	c, err := bootstrap.Load(m.Network.Candidate)
	return c.Network, err
}
