// Package outbound adapts the upstream share parser without duplicating its protocol model.
package outbound

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/xtls/libxray/share"
	"github.com/xtls/xray-core/infra/conf"
)

const ImportWarning = "Импорт отражает возможности libXray: некоторые параметры ссылки могут быть проигнорированы. Проверьте полученные настройки перед сохранением."

// Import accepts one URI only. It neither fetches subscriptions nor writes configuration.
func Import(uri string) (json.RawMessage, error) {
	uri = strings.TrimSpace(uri)
	if uri == "" || len(uri) > 64<<10 || strings.ContainsAny(uri, "\r\n") || !strings.Contains(uri, "://") || strings.HasPrefix(uri, "{") {
		return nil, errors.New("expected one supported connection URI")
	}
	raw, err := share.ConvertShareLinksToXrayJson(uri, "")
	if err != nil {
		return nil, errors.New("connection URI is unsupported or invalid")
	}
	var result struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Outbounds) != 1 {
		return nil, errors.New("connection URI is unsupported or invalid")
	}
	var native conf.OutboundDetourConfig
	if json.Unmarshal(result.Outbounds[0], &native) != nil || native.Protocol == "" {
		return nil, errors.New("connection URI is unsupported or invalid")
	}
	// A URI fragment is a display suggestion, never the identity of an existing outbound.
	return result.Outbounds[0], nil
}
