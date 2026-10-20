package xrayconfig

import (
	"bytes"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/nativebinding"
)

// BindUserGeosite renders only explicitly annotated filename references.
// It is used during preparation/apply, never as a startup migration.
func BindUserGeosite(raw []byte, name string) ([]byte, error) {
	if !bootstrap.ValidUserGeositeName(name) {
		return nil, nativebinding.ErrUnresolved
	}
	if !bytes.Contains(raw, []byte("// helper:")) {
		return bytes.Clone(raw), nil
	}
	document, err := nativebinding.Parse(raw)
	if err != nil {
		return nil, err
	}
	return document.RenderSelected(func(provider string) (string, error) {
		if provider == "helper.user-geosite" {
			return name, nil
		}
		return "", nativebinding.ErrUnresolved
	}, func(binding nativebinding.Binding) bool { return binding.Source == "helper.user-geosite" })
}
