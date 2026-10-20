package nativerender

import (
	"context"
	"os"
	"path/filepath"

	"home-router/xray-core-helper/internal/nativebinding"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatecompare"
	"home-router/xray-core-helper/internal/templatevalidation"
)

type Validator interface {
	Validate(context.Context, templatecatalog.ID, string) (templatevalidation.Result, error)
}

// ValidateStored validates a concrete backup as-is, or prepares an unresolved
// native source backup without rewriting it before template restoration.
func (renderer Renderer) ValidateStored(ctx context.Context, id templatecatalog.ID, path string, validator Validator) error {
	if id == templatecatalog.XrayConfig && renderer.Xray == nil {
		_, err := validator.Validate(ctx, id, path)
		return err
	}
	reader, err := templatecompare.NewSafeReader(templatecompare.MaxFileBytes)
	if err != nil {
		return err
	}
	raw, err := reader.ReadContent(ctx, path)
	if err != nil {
		return err
	}
	if _, err := nativebinding.Parse(raw); err == nil {
		_, err = validator.Validate(ctx, id, path)
		return err
	}
	var bound []byte
	if id == templatecatalog.XrayConfig {
		bound, err = renderer.RenderXray(ctx, raw)
	} else {
		bound, err = renderer.Render(ctx, raw)
	}
	if err != nil {
		return err
	}
	entry, err := renderer.Catalog.Lookup(id)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "helper-backup-validation-*"+filepath.Ext(entry.SystemPath))
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(bound)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	_, err = validator.Validate(ctx, id, f.Name())
	return err
}
