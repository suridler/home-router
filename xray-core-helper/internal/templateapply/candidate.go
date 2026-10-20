package templateapply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"home-router/xray-core-helper/internal/nativeblock"
	"home-router/xray-core-helper/internal/nativerender"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatecompare"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type initializeKey struct{}

// WithBlockInitialization records the operator's explicit first-install review
// of this exact template/system revision. It never authorizes arbitrary paths.
func WithBlockInitialization(ctx context.Context, revision string) context.Context {
	return context.WithValue(ctx, initializeKey{}, revision)
}

type candidate struct {
	path, hash string
	close      func()
}

func (engine *Engine) candidate(ctx context.Context, entry templatecatalog.Entry, preparing bool) (candidate, error) {
	if engine.buildCandidate != nil {
		return engine.buildCandidate(ctx, entry, preparing)
	}
	if entry.ID == templatecatalog.XrayConfig {
		source, _, _, _, _, err := readRegular(entry.WorkingPath)
		if err != nil {
			return candidate{}, &Error{Code: CodeValidation}
		}
		var bound []byte
		if engine.xrayValues != nil {
			bound, err = (nativerender.Renderer{Catalog: engine.catalog, Xray: engine.xrayValues, Logging: engine.loggingValues}).RenderXray(ctx, source)
		} else {
			bound, err = xrayconfig.BindUserGeosite(source, engine.userGeosite)
		}
		if err != nil {
			return candidate{}, &Error{Code: CodeBindingInvalid}
		}
		if !bytes.Equal(source, bound) {
			return stageCandidate(bound)
		}
	}
	var rendered []byte
	if engine.renderEnabled && entry.ID != templatecatalog.XrayConfig {
		source, _, _, _, _, err := readRegular(entry.WorkingPath)
		if err != nil {
			return candidate{}, &Error{Code: CodeValidation}
		}
		rendered, err = engine.renderNative(ctx, source)
		if err != nil {
			return candidate{}, &Error{Code: CodeBindingUnresolved}
		}
	}
	if entry.ID != templatecatalog.Nftables {
		if rendered != nil {
			return stageNativeCandidate(rendered, filepath.Ext(entry.SystemPath))
		}
		hash, err := engine.hash(entry.WorkingPath)
		return candidate{path: entry.WorkingPath, hash: hash, close: func() {}}, err
	}
	initialize := !preparing
	if preparing {
		if revision, ok := ctx.Value(initializeKey{}).(string); ok {
			comparer, _ := templatecompare.New(nil)
			if engine.renderEnabled {
				comparer.SetRenderer(func(ctx context.Context, _ templatecatalog.Entry, raw []byte) ([]byte, error) {
					return engine.renderNative(ctx, raw)
				})
			}
			status := comparer.Compare(ctx, entry)
			if revision == "" || status.Revision != revision {
				return candidate{}, &Error{Code: CodeGeneration}
			}
			initialize = true
		}
	}
	if rendered != nil {
		return mergeContent(entry, rendered, initialize)
	}
	return mergeCandidate(entry, initialize)
}

func mergeCandidate(entry templatecatalog.Entry, initialize bool) (candidate, error) {
	source, _, _, _, _, err := readRegular(entry.WorkingPath)
	if err != nil {
		return candidate{}, &Error{Code: CodeValidation}
	}
	return mergeContent(entry, source, initialize)
}

func mergeContent(entry templatecatalog.Entry, source []byte, initialize bool) (candidate, error) {
	target, _, _, _, _, err := readRegular(entry.SystemPath)
	if err != nil {
		return candidate{}, &Error{Code: CodeRelatedRead}
	}
	merged, err := nativeblock.Build(target, source, initialize)
	if errors.Is(err, nativeblock.ErrInitialize) {
		return candidate{}, &Error{Code: CodeInitialize}
	}
	if err != nil {
		return candidate{}, &Error{Code: CodeValidation}
	}
	file, err := os.CreateTemp("", "helper-native-candidate-*.conf")
	if err != nil {
		return candidate{}, &Error{Code: CodeReplace}
	}
	cleanup := func() { os.Remove(file.Name()) }
	if _, err = file.Write(merged.Content); err != nil {
		file.Close()
		cleanup()
		return candidate{}, &Error{Code: CodeReplace}
	}
	if err = file.Close(); err != nil {
		cleanup()
		return candidate{}, &Error{Code: CodeReplace}
	}
	sum := sha256.Sum256(merged.Content)
	return candidate{path: file.Name(), hash: hex.EncodeToString(sum[:]), close: cleanup}, nil
}

func stageCandidate(content []byte) (candidate, error) {
	return stageNativeCandidate(content, ".jsonc")
}

func stageNativeCandidate(content []byte, extension string) (candidate, error) {
	file, err := os.CreateTemp("", "helper-native-candidate-*"+extension)
	if err != nil {
		return candidate{}, &Error{Code: CodeReplace}
	}
	cleanup := func() { os.Remove(file.Name()) }
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		cleanup()
		return candidate{}, &Error{Code: CodeReplace}
	}
	sum := sha256.Sum256(content)
	return candidate{path: file.Name(), hash: hex.EncodeToString(sum[:]), close: cleanup}, nil
}
