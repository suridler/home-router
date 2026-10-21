package candidatevalidation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// ValidateCandidates overlays only prepared files. All other referenced assets
// come from the current installation, including results of the previous process.
func (v *Validator) ValidateCandidates(ctx context.Context, candidates map[string]string) (err error) {
	if ctx == nil {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	referenced, err := v.assetReader.ReferencedExternalAssets(ctx)
	if err != nil {
		return ErrCandidateSetup
	}
	dir, err := v.files.MkdirTemp(v.tempRoot, "xray-candidate-*")
	if err != nil {
		return ErrCandidateSetup
	}
	defer func() {
		if cleanupErr := v.files.RemoveAll(dir); cleanupErr != nil && err == nil {
			err = &CleanupWarning{}
		}
	}()
	if err := v.files.Chmod(dir, candidateDirectoryMode); err != nil {
		return ErrCandidateSetup
	}
	names := map[string]bool{}
	// Built-in geosite:/geoip: selectors do not appear as ext: references.
	// Include their current files when present, without requiring unused files.
	for _, name := range []string{"geosite.dat", "geoip.dat"} {
		if _, err := os.Stat(filepath.Join(v.assetDir, name)); err == nil {
			names[name] = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, name := range referenced {
		names[name] = true
	}
	for name := range candidates {
		if name != "geosite.dat" && name != "geoip.dat" && name != v.userGeosite && name != v.userGeoip {
			return ErrInvalidInput
		}
		names[name] = true
	}
	for name := range names {
		if name == "." || filepath.Base(name) != name {
			return ErrUnsupportedAsset
		}
		source := candidates[name]
		if source == "" {
			source = filepath.Join(v.assetDir, name)
		}
		if err := v.copyAsset(ctx, source, filepath.Join(dir, name)); err != nil {
			return errors.Join(ErrCandidateSetup, err)
		}
	}
	if err := v.runner.Run(ctx, v.binary, []string{"run", "-test", "-config", v.config}, []string{"XRAY_LOCATION_ASSET=" + dir}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrXrayValidation
	}
	return nil
}

// CleanupWarning means validation succeeded; only disposable copies remain.
type CleanupWarning struct{}

func (*CleanupWarning) Error() string { return "temporary candidate cleanup failed" }
func (*CleanupWarning) Warning() bool { return true }
