package xrayconfig

import (
	"bytes"
	"strconv"
	"strings"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/nativebinding"
)

// TemplateValues are explicit, read-only render inputs. Xray is the selected
// existing or staged config, never a second persisted copy. Missing Xray
// projections fail closed; initial defaults belong to setup's candidate builder.
type TemplateValues struct {
	Network                     bootstrap.NetworkConfig
	Logging                     bootstrap.LoggingConfig
	API, UserGeosite, UserGeoip string
	Xray                        []byte
}

func (values TemplateValues) Resolve(source string) (string, error) {
	if strings.HasPrefix(source, "helper.network.") {
		return values.Network.BindingValue(source)
	}
	switch source {
	case "helper.logging.access-path":
		return values.Logging.AccessPath, nil
	case "helper.logging.error-path":
		return values.Logging.ErrorPath, nil
	case "helper.logging.level":
		return values.Logging.Level, nil
	case "helper.logging.dns-log":
		return strconv.FormatBool(values.Logging.DNSLog), nil
	case "helper.xray-api":
		return values.API, nil
	case "helper.user-geosite":
		return values.UserGeosite, nil
	case "helper.user-geoip":
		return values.UserGeoip, nil
	case "xray.tproxy-port", "xray.egress-mark", "xray.intercept-mark":
		return BindingValue(values.Xray, source)
	}
	return "", nativebinding.ErrUnresolved
}

// RenderTemplate expands only explicitly annotated scalar placeholders and
// refreshes their concrete locators. It neither edits its input nor writes any
// files. Unknown JSONC fields and comments retain their exact source bytes.
func RenderTemplate(raw []byte, values TemplateValues) ([]byte, error) {
	if !bytes.Contains(raw, []byte("// helper:")) {
		if _, err := parseSource(raw); err != nil {
			return nil, nativebinding.ErrInvalid
		}
		return bytes.Clone(raw), nil
	}
	prepared, err := nativebinding.Prepare(raw, values.Resolve)
	if err != nil {
		return nil, err
	}
	document, err := nativebinding.Parse(prepared)
	if err != nil {
		return nil, err
	}
	rendered, err := document.Render(values.Resolve)
	if err != nil {
		return nil, err
	}
	if _, err := parseSource(rendered); err != nil {
		return nil, nativebinding.ErrInvalid
	}
	return rendered, nil
}
