package xrayconfig

import (
	"errors"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func NativeAssetDependencies(raw []byte) ([]string, error) {
	parsed, err := parseSource(raw)
	if err != nil {
		return nil, errors.New("invalid native config")
	}
	assets := map[string]bool{}
	var walk func(any) error
	walk = func(value any) error {
		switch value := value.(type) {
		case map[string]any:
			for _, v := range value {
				if err := walk(v); err != nil {
					return err
				}
			}
		case []any:
			for _, v := range value {
				if err := walk(v); err != nil {
					return err
				}
			}
		case string:
			if strings.HasPrefix(value, "geoip:") {
				assets["geoip.dat"] = true
			}
			if strings.HasPrefix(value, "geosite:") {
				assets["geosite.dat"] = true
			}
			if strings.HasPrefix(value, "ext:") {
				parts := strings.Split(value, ":")
				if len(parts) < 3 || parts[1] == "" || filepath.Base(parts[1]) != parts[1] || parts[1] == ".." {
					return errors.New("invalid external asset")
				}
				assets[parts[1]] = true
			}
		}
		return nil
	}
	if err = walk(parsed.value); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(assets))
	for asset := range assets {
		out = append(out, asset)
	}
	sort.Strings(out)
	return out, nil
}

type NativeListener struct{ Network, Address string }

func NativeIPv4Listeners(raw []byte) ([]NativeListener, error) { return nativeIPv4Listeners(raw, true) }
func NativeInboundListeners(raw []byte) ([]NativeListener, error) {
	return nativeIPv4Listeners(raw, false)
}
func nativeIPv4Listeners(raw []byte, includeAPI bool) ([]NativeListener, error) {
	parsed, err := parseSource(raw)
	if err != nil {
		return nil, errors.New("invalid config")
	}
	result := []NativeListener{}
	if includeAPI {
		endpoint, err := ExtractLocalAPI(raw)
		if err != nil {
			return nil, err
		}
		result = append(result, NativeListener{"tcp", endpoint})
	}
	for _, item := range asSlice(parsed.value["inbounds"]) {
		inbound := asMap(item)
		port, ok := relationInteger(inbound["port"])
		if !ok {
			return nil, errors.New("unsupported listener")
		}
		host := safeString(inbound["listen"])
		if host == "" {
			host = "0.0.0.0"
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() == nil {
			return nil, errors.New("unsupported listener address")
		}
		networks := []string{"tcp"}
		if safeString(inbound["protocol"]) == "dokodemo-door" {
			value := safeString(asMap(inbound["settings"])["network"])
			if value != "" {
				networks = strings.Split(value, ",")
			}
		}
		for _, network := range networks {
			if network != "tcp" && network != "udp" {
				return nil, errors.New("unsupported transport")
			}
			result = append(result, NativeListener{network, net.JoinHostPort(host, strconv.Itoa(port))})
		}
	}
	return result, nil
}
