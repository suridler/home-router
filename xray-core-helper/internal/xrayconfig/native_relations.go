package xrayconfig

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
)

// OutboundEndpoint is internal-only; callers must never serialize or log it.
func (reader Reader) OutboundEndpoint(ctx context.Context, tag string) (string, error) {
	doc, err := reader.readDocument(ctx)
	if err != nil {
		return "", errors.New("outbound unavailable")
	}
	for _, value := range asSlice(doc.value["outbounds"]) {
		outbound := asMap(value)
		if safeString(outbound["tag"]) != tag {
			continue
		}
		servers := asSlice(asMap(outbound["settings"])["vnext"])
		if len(servers) != 1 {
			return "", errors.New("outbound endpoint ambiguous")
		}
		server := asMap(servers[0])
		host := safeString(server["address"])
		port, ok := relationInteger(server["port"])
		if !ok || host == "" || len(host) > 253 {
			return "", errors.New("outbound unavailable")
		}
		return net.JoinHostPort(host, strconv.Itoa(port)), nil
	}
	return "", errors.New("outbound unavailable")
}

// ExtractLocalAPI returns the native API endpoint only when it is loopback TCP.
func ExtractLocalAPI(source []byte) (string, error) {
	parsed, err := parseSource(source)
	if err != nil {
		return "", errors.New("invalid API configuration")
	}
	endpoint := safeString(asMap(parsed.value["api"])["listen"])
	if endpoint == "" {
		tag := safeString(asMap(parsed.value["api"])["tag"])
		for _, value := range asSlice(parsed.value["inbounds"]) {
			inbound := asMap(value)
			if tag != "" && safeString(inbound["tag"]) == tag {
				if port, ok := relationInteger(inbound["port"]); ok {
					endpoint = net.JoinHostPort(safeString(inbound["listen"]), strconv.Itoa(port))
				}
			}
		}
	}
	host, port, err := net.SplitHostPort(endpoint)
	ip := net.ParseIP(host)
	number, portErr := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || portErr != nil || number < 1 || number > 65535 {
		return "", errors.New("invalid local API endpoint")
	}
	return endpoint, nil
}

func markInteger(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	n, err := number.Int64()
	return int(n), err == nil && n > 0 && n <= 1<<32-1
}

func relationInteger(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	return int(parsed), err == nil && parsed > 0 && parsed <= 65535
}
