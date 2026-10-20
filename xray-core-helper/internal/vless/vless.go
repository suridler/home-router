package vless

import (
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const maxURILength = 4096

type TransportKey string

const (
	TransportKeyNetwork TransportKey = "network"
	TransportKeyMethod  TransportKey = "method"
)

var supportedParameters = map[string]struct{}{
	"type": {}, "security": {}, "flow": {}, "encryption": {}, "pbk": {}, "fp": {}, "sni": {},
	"sid": {}, "spx": {}, "alpn": {}, "path": {}, "mode": {}, "host": {}, "xPaddingBytes": {},
	"sc": {}, "serviceName": {},
}

// Preview is the credential-free summary of a VLESS URI.
type Preview struct {
	Address               string   `json:"address"`
	Port                  int      `json:"port"`
	Transport             string   `json:"transport"`
	Security              string   `json:"security"`
	Flow                  string   `json:"flow"`
	ServerName            string   `json:"serverName"`
	UnsupportedParameters []string `json:"unsupportedParameters"`
}

// Parsed holds the Xray outbound fragments built from a URI; it carries secrets.
type Parsed struct {
	Settings       map[string]any
	StreamSettings map[string]any
	Preview        Preview
}

func Parse(raw string, key TransportKey) (Parsed, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxURILength {
		return Parsed{}, errors.New("VLESS URI is invalid")
	}
	uri, err := url.Parse(raw)
	if err != nil {
		return Parsed{}, errors.New("VLESS URI is invalid")
	}
	if uri.Scheme != "vless" {
		return Parsed{}, errors.New("URI must use the vless:// scheme")
	}
	query, err := url.ParseQuery(uri.RawQuery)
	if err != nil {
		return Parsed{}, errors.New("VLESS URI is invalid")
	}
	first := func(name, fallback string) string {
		if values, ok := query[name]; ok && len(values) > 0 {
			return values[0]
		}
		return fallback
	}
	has := func(name string) bool { _, ok := query[name]; return ok }

	id := ""
	if uri.User != nil {
		id = uri.User.Username()
	}
	address := uri.Hostname()
	port := 443
	if portText := uri.Port(); portText != "" {
		port, err = strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return Parsed{}, errors.New("VLESS URI has an invalid port")
		}
	}
	if id == "" {
		return Parsed{}, errors.New("VLESS URI must include a user ID")
	}
	if address == "" {
		return Parsed{}, errors.New("VLESS URI must include a server address")
	}

	transportType := first("type", "tcp")
	security := first("security", "none")
	unsupported := make([]string, 0)
	for name := range query {
		if _, ok := supportedParameters[name]; !ok {
			unsupported = append(unsupported, name)
		}
	}
	sort.Strings(unsupported)

	transport := transportType
	if key == TransportKeyMethod {
		switch transportType {
		case "tcp":
			transport = "raw"
		case "ws":
			transport = "websocket"
		}
	}
	stream := map[string]any{string(key): transport, "security": security}

	if transportType == "xhttp" {
		xhttp := map[string]any{}
		for _, name := range []string{"path", "mode", "host"} {
			if has(name) {
				xhttp[name] = first(name, "")
			}
		}
		extra := map[string]any{}
		if has("xPaddingBytes") {
			extra["xPaddingBytes"] = first("xPaddingBytes", "")
		}
		if has("sc") {
			extra["speedControl"] = first("sc", "")
		}
		if len(extra) > 0 {
			xhttp["extra"] = extra
		}
		stream["xhttpSettings"] = xhttp
	}

	switch security {
	case "reality":
		reality := map[string]any{
			"publicKey":   first("pbk", ""),
			"fingerprint": first("fp", "chrome"),
			"serverName":  first("sni", ""),
			"shortId":     first("sid", ""),
			"spiderX":     first("spx", "/"),
		}
		if has("alpn") {
			reality["alpn"] = splitList(first("alpn", ""))
		}
		stream["realitySettings"] = reality
	case "tls":
		tls := map[string]any{"serverName": first("sni", ""), "fingerprint": first("fp", "chrome")}
		if has("alpn") {
			tls["alpn"] = splitList(first("alpn", ""))
		}
		stream["tlsSettings"] = tls
	}

	switch {
	case transportType == "grpc" && has("serviceName"):
		stream["grpcSettings"] = map[string]any{"serviceName": first("serviceName", "")}
	case transportType == "ws":
		stream["wsSettings"] = map[string]any{
			"path":    first("path", "/"),
			"headers": map[string]any{"Host": first("sni", "")},
		}
	}

	flow := first("flow", "")
	return Parsed{
		Settings: map[string]any{"vnext": []any{map[string]any{
			"address": address,
			"port":    port,
			"users":   []any{map[string]any{"id": id, "flow": flow, "encryption": first("encryption", "none")}},
		}}},
		StreamSettings: stream,
		Preview: Preview{
			Address:               address,
			Port:                  port,
			Transport:             transportType,
			Security:              security,
			Flow:                  defaultText(flow, "not configured"),
			ServerName:            defaultText(first("sni", ""), "not configured"),
			UnsupportedParameters: unsupported,
		},
	}, nil
}

func splitList(value string) []string {
	items := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func defaultText(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
