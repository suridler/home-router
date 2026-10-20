package consolidation

import (
	"context"
	"errors"
	"os"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/geodatainstall"
)

// The two views share one store, manifest, frozen snapshot and baseline commit.
// Views reuse only bounded single-file I/O; they never run independent lifecycles.
type assetView struct {
	service  *Service
	manifest Manifest
}

func (s *Service) ipService() *Service {
	view := *s
	view.candidateName = s.geoipName
	view.backupName = s.geoipName + ".previous"
	view.liveCandidateName = "." + s.geoipName + ".xch-candidate"
	return &view
}

func ipRecord(m Manifest, backup string) geodatainstall.AssetRecord {
	return geodatainstall.AssetRecord{Name: m.GeositeName, InstalledHash: m.CandidateHash,
		PreviousExists: m.PreviousExists, PreviousHash: m.PreviousHash, BackupName: backup,
		PreviousMode: m.PreviousMode, PreviousUID: m.PreviousUID, PreviousGID: m.PreviousGID}
}

func (s *Service) assets(m Manifest) []assetView {
	ip := m.Geoip
	return []assetView{{s, m}, {s.ipService(), Manifest{
		GeositeName: ip.Name, CandidateHash: ip.InstalledHash, PreviousExists: ip.PreviousExists,
		PreviousHash: ip.PreviousHash, PreviousMode: ip.PreviousMode, PreviousUID: ip.PreviousUID, PreviousGID: ip.PreviousGID,
	}}}
}

func validateIPRecord(ip geodatainstall.AssetRecord) error {
	if !bootstrap.ValidUserGeoipName(ip.Name) || ip.BackupName != ip.Name+".previous" || !validHash(ip.InstalledHash) {
		return ErrManifest
	}
	if ip.PreviousExists {
		if !validHash(ip.PreviousHash) || ip.PreviousUID < 0 || ip.PreviousGID < 0 || uint64(ip.PreviousUID) > uint64(^uint32(0)) || uint64(ip.PreviousGID) > uint64(^uint32(0)) || ip.PreviousMode & ^uint32(07777) != 0 {
			return ErrManifest
		}
	} else if ip.PreviousHash != "" || ip.PreviousMode != 0 || ip.PreviousUID != 0 || ip.PreviousGID != 0 {
		return ErrManifest
	}
	return nil
}

func (s *Service) assetPhase(ctx context.Context, dir geodatainstall.Directory, m Manifest) (LiveGeositePhase, error) {
	hash, exists, err := s.hashOptionalContext(ctx, dir, s.candidateName)
	if err != nil {
		return LiveGeositeDrift, err
	}
	if exists && hash == m.CandidateHash {
		expected := m
		if !m.PreviousExists {
			expected.PreviousMode = installedMode
			expected.PreviousUID = os.Geteuid()
			expected.PreviousGID = os.Getegid()
		}
		if err := s.verifyLivePrevious(dir, expected); err != nil {
			return LiveGeositeDrift, nil
		}
		return LiveGeositeCandidate, nil
	}
	if exists && m.PreviousExists && hash == m.PreviousHash {
		if err := s.verifyLivePrevious(dir, m); err != nil {
			return LiveGeositeDrift, nil
		}
		return LiveGeositePrevious, nil
	}
	if !exists && !m.PreviousExists {
		return LiveGeositeAbsent, nil
	}
	return LiveGeositeDrift, nil
}

func (s *Service) pairPhase(ctx context.Context, dir geodatainstall.Directory, m Manifest) (LiveGeositePhase, error) {
	allCandidate, allPrevious, allAbsent := true, true, true
	for _, asset := range s.assets(m) {
		phase, err := asset.service.assetPhase(ctx, dir, asset.manifest)
		if err != nil {
			return LiveGeositeDrift, err
		}
		if phase == LiveGeositeDrift {
			return phase, nil
		}
		allCandidate = allCandidate && phase == LiveGeositeCandidate
		allPrevious = allPrevious && (phase == LiveGeositePrevious || phase == LiveGeositeAbsent)
		allAbsent = allAbsent && phase == LiveGeositeAbsent
	}
	switch {
	case allCandidate:
		return LiveGeositeCandidate, nil
	case allAbsent:
		return LiveGeositeAbsent, nil
	case allPrevious:
		return LiveGeositePrevious, nil
	default:
		return LiveAssetsMixed, nil
	}
}

func (s *Service) checkPairCandidates(ctx context.Context, dir geodatainstall.Directory, m Manifest) error {
	phase, err := s.pairPhase(ctx, dir, m)
	if err != nil {
		return err
	}
	if phase != LiveGeositeCandidate {
		return ErrExternalDrift
	}
	return nil
}

func (s *Service) checkPairPrevious(ctx context.Context, dir geodatainstall.Directory, m Manifest) error {
	for _, asset := range s.assets(m) {
		hash, exists, err := asset.service.hashOptionalContext(ctx, dir, asset.service.candidateName)
		if err != nil {
			return err
		}
		if asset.manifest.PreviousExists {
			if !exists || hash != asset.manifest.PreviousHash {
				return ErrExternalDrift
			}
			if err := asset.service.verifyLivePrevious(dir, asset.manifest); err != nil {
				return err
			}
		} else if exists {
			return ErrExternalDrift
		}
	}
	return nil
}

func (s *Service) rollbackPair(ctx context.Context, live, stage geodatainstall.Directory, m Manifest) error {
	views := s.assets(m)
	restore := make([]bool, len(views))
	// Validate the complete pair before modifying any live member.
	for i, asset := range views {
		phase, err := asset.service.assetPhase(ctx, live, asset.manifest)
		if err != nil {
			return err
		}
		if phase == LiveGeositeDrift {
			return ErrExternalDrift
		}
		restore[i] = phase == LiveGeositeCandidate
	}
	for _, asset := range views {
		if err := asset.service.removeInterruptedCandidate(live, asset.manifest); err != nil {
			return err
		}
	}
	for i, asset := range views {
		if !restore[i] {
			continue
		}
		if asset.manifest.PreviousExists {
			backup, hash, err := asset.service.readBoundedRegular(stage, asset.service.backupName)
			if err != nil || hash != asset.manifest.PreviousHash {
				return ErrManifest
			}
			if err := asset.service.atomicRestore(live, backup, asset.manifest); err != nil {
				return errors.Join(ErrRollback, err)
			}
		} else {
			if err := live.Remove(asset.service.candidateName); err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.Join(ErrRollback, err)
			}
			if err := live.Sync(); err != nil {
				return errors.Join(ErrRollback, err)
			}
		}
	}
	return nil
}
