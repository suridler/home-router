package templateapply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"home-router/xray-core-helper/internal/templatecatalog"
)

// These identifiers are recovery evidence only; they are not catalog/API IDs.
const (
	LifecycleScriptID templatecatalog.ID = "tproxy-network-script"
	LifecycleDropinID templatecatalog.ID = "tproxy-xray-dropin"
	LifecycleSysctlID templatecatalog.ID = "tproxy-sysctl-unit"
)

type LifecycleAsset struct {
	ID             templatecatalog.ID
	Source, Target string
	Mode           os.FileMode
	UID, GID       int
}

// DefaultLifecycleAssets is the fixed installation inventory, never supplied by
// an API caller. Bundle upgrades replace the sources, not these destinations.
func DefaultLifecycleAssets() []LifecycleAsset {
	return []LifecycleAsset{
		{LifecycleScriptID, "/usr/share/xray-core-helper/native/xray-tproxy-network.py", "/usr/local/libexec/xray-tproxy/network", 0755, 0, 0},
		{LifecycleDropinID, "/usr/share/xray-core-helper/native/50-tproxy.conf", "/etc/systemd/system/xray.service.d/50-tproxy.conf", 0644, 0, 0},
		{LifecycleSysctlID, "/usr/share/xray-core-helper/native/xray-tproxy-sysctl.service", "/etc/systemd/system/xray-tproxy-sysctl.service", 0644, 0, 0},
	}
}

func LifecycleMember(id templatecatalog.ID) bool {
	return id == LifecycleScriptID || id == LifecycleDropinID || id == LifecycleSysctlID
}

func (engine *Engine) lifecycleInventory() []LifecycleAsset {
	if engine.lifecycleAssets != nil {
		return engine.lifecycleAssets
	}
	// Hermetic engines substitute candidates/group paths. They must explicitly
	// provide their auxiliary inventory rather than reach into host paths.
	if engine.buildCandidate != nil || engine.groupPaths != nil {
		return nil
	}
	return DefaultLifecycleAssets()
}

type lifecyclePrepared struct {
	assets []BaselineFile
	prior  []BaselineFile
}

func (engine *Engine) prepareLifecycle() (lifecyclePrepared, error) {
	var result lifecyclePrepared
	for _, asset := range engine.lifecycleInventory() {
		raw, _, _, _, _, err := readRegular(asset.Source)
		if err != nil || len(raw) > 1<<20 || len(raw) == 0 {
			return result, &Error{Code: CodeRelatedRead}
		}
		prior, err := captureBaselineFile(asset.ID, asset.Target)
		if err != nil {
			return result, err
		}
		result.prior = append(result.prior, prior)
		result.assets = append(result.assets, BaselineFile{TemplateID: asset.ID, Present: true, Content: raw, Mode: uint32(asset.Mode), UID: uint32(asset.UID), GID: uint32(asset.GID)})
	}
	return result, nil
}

func lifecycleHash(candidateHash string, prepared lifecyclePrepared) string {
	if len(prepared.assets) == 0 {
		return candidateHash
	}
	h := sha256.New()
	h.Write([]byte(candidateHash))
	for _, asset := range prepared.assets {
		h.Write([]byte{0})
		h.Write([]byte(asset.TemplateID))
		h.Write([]byte{0})
		h.Write(asset.Content)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (engine *Engine) lifecyclePath(id templatecatalog.ID) (string, error) {
	for _, asset := range engine.lifecycleInventory() {
		if asset.ID == id {
			return asset.Target, nil
		}
	}
	return "", &Error{Code: CodeRelatedRead}
}

func (engine *Engine) installLifecycle(ctx context.Context, prepared lifecyclePrepared) error {
	// Check every target before writing the first asset.
	for _, prior := range prepared.prior {
		path, err := engine.lifecyclePath(prior.TemplateID)
		if err != nil {
			return err
		}
		actual, err := captureBaselineFile(prior.TemplateID, path)
		if err != nil || !baselineEqual(prior, actual) {
			return &Error{Code: CodeValidation}
		}
	}
	for _, asset := range prepared.assets {
		if err := ctx.Err(); err != nil {
			return err
		}
		path, err := engine.lifecyclePath(asset.TemplateID)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return &Error{Code: CodeReplace}
		}
		if err = atomicReplace(path, asset.Content, os.FileMode(asset.Mode), int(asset.UID), int(asset.GID)); err != nil {
			return &Error{Code: CodeReplace}
		}
	}
	return nil
}

func (engine *Engine) verifyLifecycle(prepared lifecyclePrepared) error {
	for _, expected := range prepared.assets {
		path, err := engine.lifecyclePath(expected.TemplateID)
		if err != nil {
			return err
		}
		actual, err := captureBaselineFile(expected.TemplateID, path)
		if err != nil || !baselineEqual(expected, actual) {
			return &Error{Code: CodeValidation}
		}
	}
	return nil
}

func baselineEqual(a, b BaselineFile) bool {
	return a.TemplateID == b.TemplateID && a.Present == b.Present && a.Mode == b.Mode && a.UID == b.UID && a.GID == b.GID && bytes.Equal(a.Content, b.Content)
}
