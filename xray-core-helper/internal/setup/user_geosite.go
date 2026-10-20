package setup

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/commandrunner"
	"home-router/xray-core-helper/internal/geodatalimits"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/usergeoip"
	"home-router/xray-core-helper/internal/xrayconfig"
)

// setupAsset binds additive asset creation to the durable setup transaction.
// Existing files are only read and fingerprinted, never regenerated or renamed.
type setupAsset struct {
	Name      string `json:"name"`
	Directory string `json:"directory"`
	Hash      string `json:"hash"`
	Existed   bool   `json:"existed"`
}

func (s *fileStore) configureGeosite(assetDir, binary, builder string) error {
	for _, path := range []string{assetDir, binary, builder} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("invalid setup geosite settings")
		}
	}
	s.assetDir, s.geositeBuilder = assetDir, builder
	s.validateAssets = func(ctx context.Context, config, assets string) error {
		validator, err := transaction.NewXrayValidator(binary, assets)
		if err != nil {
			return err
		}
		bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return validator.Validate(bounded, config)
	}
	return nil
}

func (s *fileStore) readSetupAsset(path string) ([]byte, error) {
	if err := safePath(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 {
		return nil, errors.New("invalid setup asset")
	}
	if info.Size() > int64(s.maxAssetSize) {
		return nil, &geodatalimits.Exceeded{LimitBytes: s.maxAssetSize}
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(s.maxAssetSize)+1))
	if err != nil || len(raw) > s.maxAssetSize {
		return nil, errors.New("invalid setup asset")
	}
	return raw, nil
}

func (s *fileStore) geositePath() string { return filepath.Join(s.assetDir, s.userGeosite) }
func (s *fileStore) geositeCandidate(m setupManifest) string {
	return filepath.Join(s.root, m.ID, "user-geosite", s.userGeosite)
}

func (s *fileStore) prepareGeosite(ctx context.Context, m *setupManifest) error {
	raw, err := s.readSetupAsset(s.geositePath())
	if err == nil {
		m.UserGeosite = &setupAsset{Directory: s.assetDir, Name: s.userGeosite, Hash: contentHash(raw), Existed: true}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(s.geositeCandidate(*m))
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	// The reserved .invalid domain keeps USERLIST valid without real entries.
	if err := s.replace(filepath.Join(dir, "userlist"), []byte("domain:xray-core-helper-empty-domain.invalid\n"), 0600, os.Geteuid(), os.Getegid()); err != nil {
		return err
	}
	runner, err := commandrunner.New(s.geositeBuilder, 30*time.Second)
	if err != nil {
		return err
	}
	if _, err := runner.Run(ctx, []string{"-datapath", dir, "-outputname", s.userGeosite, "-outputdir", dir}); err != nil {
		return errors.New("setup user geosite build failed")
	}
	raw, err = s.readSetupAsset(s.geositeCandidate(*m))
	if err != nil {
		return err
	}
	if err := s.replace(s.geositeCandidate(*m), raw, 0600, os.Geteuid(), os.Getegid()); err != nil {
		return err
	}
	m.UserGeosite = &setupAsset{Directory: s.assetDir, Name: s.userGeosite, Hash: contentHash(raw)}
	return nil
}

// setupAssets returns the assets covered by the same durable setup manifest.
func setupAssets(m setupManifest) []*setupAsset {
	var assets []*setupAsset
	if m.UserGeosite != nil {
		assets = append(assets, m.UserGeosite)
	}
	if m.UserGeoip != nil {
		assets = append(assets, m.UserGeoip)
	}
	return assets
}

func (s *fileStore) assetCandidate(m setupManifest, asset *setupAsset) string {
	kind := "user-geosite"
	if asset == m.UserGeoip {
		kind = "user-geoip"
	}
	return filepath.Join(s.root, m.ID, kind, asset.Name)
}

// prepareGeoip creates an empty category only when the candidate references it.
// No fabricated IP or startup mutation is needed.
func (s *fileStore) prepareGeoip(m *setupManifest) error {
	required := false
	for _, item := range m.Items {
		if item.ID != templatecatalog.XrayConfig {
			continue
		}
		raw, _, err := readSafe(item.Candidate)
		if err != nil {
			return err
		}
		names, err := xrayconfig.NativeAssetDependencies(raw)
		if err != nil {
			return err
		}
		for _, name := range names {
			if name == s.userGeoip {
				required = true
			}
		}
		break
	}
	if !required {
		return nil
	}
	if !bootstrap.ValidUserGeoipName(s.userGeoip) {
		return errors.New("invalid setup user geoip name")
	}
	asset := &setupAsset{Directory: s.assetDir, Name: s.userGeoip}
	raw, err := s.readSetupAsset(filepath.Join(s.assetDir, s.userGeoip))
	if err == nil {
		asset.Existed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		raw, err = usergeoip.Encode(nil)
		if err != nil {
			return err
		}
		dir := filepath.Join(s.root, m.ID, "user-geoip")
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		if err := s.replace(filepath.Join(dir, s.userGeoip), raw, 0600, os.Geteuid(), os.Getegid()); err != nil {
			return err
		}
	}
	asset.Hash = contentHash(raw)
	m.UserGeoip = asset
	m.Version = 5
	return nil
}

func (s *fileStore) validateAssetManifest(m setupManifest) error {
	if m.UserGeosite == nil && (m.Version == 3 || m.Version >= 4 && s.assetDir != "") {
		return errors.New("missing setup geosite evidence")
	}
	if (m.Version == 5) != (m.UserGeoip != nil) {
		return errors.New("invalid setup geoip evidence")
	}
	for _, asset := range setupAssets(m) {
		validName := bootstrap.ValidUserGeositeName(asset.Name) && asset.Name == s.userGeosite
		if asset == m.UserGeoip {
			validName = bootstrap.ValidUserGeoipName(asset.Name) && asset.Name == s.userGeoip
		}
		hash, err := hex.DecodeString(asset.Hash)
		if m.Version < 3 || m.Version > 5 || s.assetDir == "" || !validName || asset.Directory != s.assetDir || err != nil || len(hash) != 32 {
			return errors.New("invalid setup asset evidence")
		}
		if err := safePath(filepath.Join(s.assetDir, asset.Name)); err != nil {
			return err
		}
	}
	return nil
}

func (s *fileStore) checkUserAssets(m setupManifest, allowAbsent bool) error {
	for _, asset := range setupAssets(m) {
		raw, err := s.readSetupAsset(filepath.Join(s.assetDir, asset.Name))
		if errors.Is(err, os.ErrNotExist) && !asset.Existed && allowAbsent {
			continue
		}
		if err != nil || contentHash(raw) != asset.Hash {
			return errors.New("setup user asset changed")
		}
	}
	return nil
}

func (s *fileStore) validateWithUserAssets(ctx context.Context, m setupManifest, config string) error {
	if m.UserGeosite == nil || s.validateAssets == nil {
		return s.validate(ctx, "xray-config", config)
	}
	raw, _, err := readSafe(config)
	if err != nil {
		return err
	}
	names, err := xrayconfig.NativeAssetDependencies(raw)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp(filepath.Join(s.root, m.ID), "validation-assets-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, name := range names {
		source := filepath.Join(s.assetDir, name)
		var expected *setupAsset
		for _, asset := range setupAssets(m) {
			if name == asset.Name {
				expected = asset
				if !asset.Existed {
					source = s.assetCandidate(m, asset)
				}
				break
			}
		}
		data, err := s.readSetupAsset(source)
		if err != nil {
			return err
		}
		if expected != nil && contentHash(data) != expected.Hash {
			return errors.New("setup user asset candidate changed")
		}
		if err := s.replace(filepath.Join(dir, name), data, 0600, os.Geteuid(), os.Getegid()); err != nil {
			return err
		}
	}
	return s.validateAssets(ctx, config, dir)
}

func (s *fileStore) publishUserAssets(m setupManifest) error {
	// Recheck the complete pair before publishing the first member.
	if err := s.checkUserAssets(m, true); err != nil {
		return err
	}
	for _, asset := range setupAssets(m) {
		if asset.Existed {
			continue
		}
		raw, err := s.readSetupAsset(s.assetCandidate(m, asset))
		if err != nil || contentHash(raw) != asset.Hash {
			return errors.New("setup user asset candidate changed")
		}
	}
	for _, asset := range setupAssets(m) {
		if err := s.publishAsset(m, asset); err != nil {
			return err
		}
	}
	return nil
}

func (s *fileStore) publishAsset(m setupManifest, asset *setupAsset) error {
	path := filepath.Join(s.assetDir, asset.Name)
	if asset.Existed {
		return nil
	}
	raw, err := s.readSetupAsset(s.assetCandidate(m, asset))
	if err != nil || contentHash(raw) != asset.Hash {
		return errors.New("setup user asset candidate changed")
	}
	if err := safePath(s.assetDir); err != nil {
		return err
	}
	file, err := os.CreateTemp(s.assetDir, ".setup-user-asset-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0644); err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	stage := "geosite-publish"
	if asset == m.UserGeoip {
		stage = "geoip-publish"
	}
	if err := s.fail(stage, path); err != nil {
		return err
	}
	// Link publishes without replacing a file created after the drift check.
	if err := os.Link(file.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrRecoveryRequired
		}
		return err
	}
	return syncDir(s.assetDir)
}

func (s *fileStore) restoreUserAssets(m setupManifest) error {
	// Verify both live assets before removing any member; retain all evidence on drift.
	if err := s.checkUserAssets(m, true); err != nil {
		return err
	}
	for _, asset := range setupAssets(m) {
		if asset.Existed {
			continue
		}
		if err := os.Remove(filepath.Join(s.assetDir, asset.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := syncDir(s.assetDir); err != nil {
			return err
		}
	}
	return nil
}
