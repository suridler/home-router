package nativebinding

import (
	"encoding/json"
	"net/netip"
	"strconv"

	"home-router/xray-core-helper/internal/bootstrap"
)

func isGeoReference(kind string) bool {
	return kind == "geosite-reference" || kind == "geoip-reference"
}

func jsonOnlyType(kind string) bool {
	switch kind {
	case "geoip-reference", "ipv4-address", "loopback-endpoint", "log-destination", "log-level", "boolean":
		return true
	}
	return false
}

func normalizeJSONValue(kind, value string) (string, error) {
	switch kind {
	case "ipv4-address":
		address, err := netip.ParseAddr(value)
		if err == nil && address.Is4() && !address.IsUnspecified() && !address.IsMulticast() {
			return address.String(), nil
		}
	case "loopback-endpoint":
		endpoint, err := netip.ParseAddrPort(value)
		if err == nil && endpoint.Addr().IsLoopback() && endpoint.Addr().Zone() == "" && endpoint.Port() != 0 {
			return endpoint.String(), nil
		}
	case "log-destination":
		if bootstrap.ValidLogDestination(value) {
			return value, nil
		}
	case "log-level":
		if bootstrap.ValidLogLevel(value) {
			return value, nil
		}
	case "boolean":
		if value == "true" || value == "false" {
			return value, nil
		}
	}
	return "", ErrInvalid
}

func jsonStringType(kind string) bool {
	return kind != "uint32" && kind != "port" && kind != "bool01" && kind != "boolean"
}

func encodeJSONScalar(kind, value string) string {
	if jsonStringType(kind) {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	return value
}

func validJSONScalar(kind, token string) bool {
	if !json.Valid([]byte(token)) {
		return false
	}
	if jsonStringType(kind) {
		_, err := strconv.Unquote(token)
		return err == nil && len(token) >= 2 && token[0] == '"'
	}
	if kind == "boolean" {
		return token == "true" || token == "false"
	}
	// JSON numbers must be integral, unsigned and unquoted. normalize enforces
	// range; this additionally rejects native hex and quoted numeric spellings.
	_, err := strconv.ParseUint(token, 10, 32)
	return err == nil
}
