package nativerender

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/nativebinding"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatecompare"
	"home-router/xray-core-helper/internal/xrayconfig"
	"home-router/xray-core-helper/internal/xrayidentity"
)

type Renderer struct {
	Network bootstrap.NetworkConfig
	Catalog templatecatalog.Catalog
	Xray    *xrayconfig.TemplateValues
	Logging func() bootstrap.LoggingConfig
}

// RenderXray uses the service-selected config for Xray projections and helper
// settings for declared helper inputs. It never infers missing live values.
func (renderer Renderer) RenderXray(ctx context.Context, raw []byte) ([]byte, error) {
	if renderer.Xray == nil {
		return nil, nativebinding.ErrUnresolved
	}
	values := *renderer.Xray
	if renderer.Logging != nil {
		values.Logging = renderer.Logging()
	}
	if !bytes.Contains(raw, []byte("// helper:")) {
		return xrayconfig.RenderTemplate(raw, values)
	}
	sources, err := nativebinding.TemplateSources(raw)
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		if !strings.HasPrefix(source, "xray.") {
			continue
		}
		entry, err := renderer.Catalog.Lookup(templatecatalog.XrayConfig)
		if err != nil {
			return nil, nativebinding.ErrUnresolved
		}
		reader, err := templatecompare.NewSafeReader(templatecompare.MaxFileBytes)
		if err != nil {
			return nil, err
		}
		values.Xray, err = reader.ReadContent(ctx, entry.SystemPath)
		if err != nil {
			return nil, nativebinding.ErrUnresolved
		}
		break
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return xrayconfig.RenderTemplate(raw, values)
}

func (renderer Renderer) Render(ctx context.Context, raw []byte) ([]byte, error) {
	resolve := func(source string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		switch source {
		case "setup.interface":
			return renderer.Network.BindingValue("helper.network.interface")
		case "setup.ipv4-prefix":
			return renderer.Network.BindingValue("helper.network.ipv4-prefix")
		case "xray.service-gid":
			gid, err := xrayidentity.New().GID(ctx)
			return fmt.Sprint(gid), err
		}
		if strings.HasPrefix(source, "helper.network.") {
			return renderer.Network.BindingValue(source)
		}
		if strings.HasPrefix(source, "xray.") {
			entry, err := renderer.Catalog.Lookup(templatecatalog.XrayConfig)
			if err != nil {
				return "", err
			}
			reader, err := templatecompare.NewSafeReader(templatecompare.MaxFileBytes)
			if err != nil {
				return "", err
			}
			raw, err := reader.ReadContent(ctx, entry.SystemPath)
			if err != nil {
				return "", err
			}
			return xrayconfig.BindingValue(raw, source)
		}
		return "", nativebinding.ErrUnresolved
	}
	prepared, err := nativebinding.Prepare(raw, resolve)
	if err != nil {
		return nil, err
	}
	doc, err := nativebinding.Parse(prepared)
	if err != nil {
		return nil, err
	}
	return doc.RenderSelected(resolve, func(b nativebinding.Binding) bool {
		return strings.HasPrefix(b.Source, "helper.network.") || strings.HasPrefix(b.Source, "setup.") || strings.HasPrefix(b.Source, "xray.")
	})
}
