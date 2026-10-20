package xrayconfig

import (
	"errors"
	"strconv"
)

// BindingValue supplies only an explicitly requested scalar from structured
// Xray JSONC. It does not impose nft/unit structure or installation defaults.
func BindingValue(source []byte, provider string) (string, error) {
	parsed, err := parseSource(source)
	if err != nil {
		return "", errors.New("xray_binding_unresolved")
	}
	values := map[int]bool{}
	switch provider {
	case "xray.tproxy-port":
		count := 0
		for _, raw := range asSlice(parsed.value["inbounds"]) {
			in := asMap(raw)
			if safeString(in["tag"]) != "tproxy-in" {
				continue
			}
			port, ok := relationInteger(in["port"])
			count++
			if !ok {
				return "", errors.New("xray_binding_unresolved")
			}
			values[port] = true
		}
		if count != 1 {
			return "", errors.New("xray_binding_unresolved")
		}
	case "xray.egress-mark", "xray.intercept-mark":
		section := "outbounds"
		if provider == "xray.intercept-mark" {
			section = "inbounds"
		}
		for _, raw := range asSlice(parsed.value[section]) {
			item := asMap(raw)
			if section == "inbounds" && safeString(item["tag"]) != "tproxy-in" {
				continue
			}
			option := asMap(asMap(item["streamSettings"])["sockopt"])
			if value, exists := option["mark"]; exists {
				mark, ok := markInteger(value)
				if !ok {
					return "", errors.New("xray_binding_unresolved")
				}
				values[mark] = true
			}
		}
	default:
		return "", errors.New("xray_binding_unresolved")
	}
	if len(values) != 1 {
		return "", errors.New("xray_binding_unresolved")
	}
	for value := range values {
		return strconv.Itoa(value), nil
	}
	return "", errors.New("xray_binding_unresolved")
}
