package xrayconfig

import (
	"bytes"
	"errors"
	"home-router/xray-core-helper/internal/transaction"
	"strconv"
	"strings"
)

// Gateway contains only non-secret gateway settings and planned edit identifiers.
type Gateway struct {
	Port       int      `json:"port"`
	EgressMark int      `json:"egressMark"`
	ProxyState string   `json:"proxyState"`
	Changes    []string `json:"changes"`
}

var ErrGateway = errors.New("xray_gateway_review_required")

// PrepareGateway fills missing gateway fields without rebuilding existing JSONC.
// Ambiguous or incompatible existing values require review, never replacement.
func PrepareGateway(raw, defaults []byte) ([]byte, Gateway, error) {
	if _, err := InspectGateway(defaults); err != nil {
		return nil, Gateway{}, ErrGateway
	}
	factory, err := parseSource(defaults)
	if err != nil {
		return nil, Gateway{}, ErrGateway
	}
	work := newSession(raw)
	if _, err := parseSource(raw); err != nil {
		return nil, Gateway{}, ErrGateway
	}
	changes := []string{}
	set := func(path []string, value any) error {
		err := work.apply(func(e *editor) ([]transaction.ByteEdit, error) {
			node := e.syntax.spans
			for _, key := range path[:len(path)-1] {
				if node.object != nil {
					node = node.object[key]
				} else {
					index, er := strconv.Atoi(key)
					if er != nil || index < 0 || index >= len(node.array) {
						return nil, ErrGateway
					}
					node = node.array[index]
				}
				if node == nil {
					return nil, ErrGateway
				}
			}
			key := path[len(path)-1]
			var edit transaction.ByteEdit
			var err error
			if n := node.object[key]; n != nil {
				edit, err = e.replaceNode(n, value)
			} else {
				edit, err = e.appendObjectMember(node, key, value)
			}
			return []transaction.ByteEdit{edit}, err
		})
		if err == nil {
			changes = append(changes, strings.Join(path, "."))
		}
		return err
	}
	appendItem := func(section string, value any) error {
		return work.apply(func(e *editor) ([]transaction.ByteEdit, error) {
			edit, err := e.appendArrayItem(e.syntax.spans.object[section], value)
			return []transaction.ByteEdit{edit}, err
		})
	}
	current, _ := parseSource(work.current)
	for _, key := range []string{"api", "stats", "policy", "dns", "routing", "inbounds", "outbounds"} {
		if _, ok := current.value[key]; !ok {
			if err := set([]string{key}, factory.value[key]); err != nil {
				return nil, Gateway{}, ErrGateway
			}
		}
	}
	current, _ = parseSource(work.current)
	idx := -1
	for i, item := range asSlice(current.value["inbounds"]) {
		in := asMap(item)
		transparent := safeString(asMap(asMap(in["streamSettings"])["sockopt"])["tproxy"]) == "tproxy"
		if transparent && safeString(in["tag"]) != "tproxy-in" {
			return nil, Gateway{}, ErrGateway
		}
		if safeString(in["tag"]) == "tproxy-in" {
			if idx >= 0 {
				return nil, Gateway{}, ErrGateway
			}
			idx = i
		}
	}
	if idx < 0 {
		if err := appendItem("inbounds", asSlice(factory.value["inbounds"])[0]); err != nil {
			return nil, Gateway{}, ErrGateway
		}
		changes = append(changes, "inbounds.tproxy-in")
	} else {
		required := []struct {
			path  []string
			value any
		}{
			{[]string{"protocol"}, "dokodemo-door"}, {[]string{"port"}, asMap(asSlice(factory.value["inbounds"])[0])["port"]},
			{[]string{"settings", "network"}, "tcp,udp"}, {[]string{"settings", "followRedirect"}, true}, {[]string{"streamSettings", "sockopt", "tproxy"}, "tproxy"},
		}
		for _, field := range required {
			current, _ = parseSource(work.current)
			value := asSlice(current.value["inbounds"])[idx]
			for n, key := range field.path {
				object, ok := value.(map[string]any)
				if !ok {
					return nil, Gateway{}, ErrGateway
				}
				next, exists := object[key]
				if !exists {
					replacement := field.value
					for j := len(field.path) - 1; j > n; j-- {
						replacement = map[string]any{field.path[j]: replacement}
					}
					path := append([]string{"inbounds", strconv.Itoa(idx)}, field.path[:n+1]...)
					if err := set(path, replacement); err != nil {
						return nil, Gateway{}, ErrGateway
					}
					break
				}
				value = next
				if n == len(field.path)-1 {
					valid := value == field.value
					if key == "port" {
						_, valid = relationInteger(value)
					}
					if key == "network" {
						s := safeString(value)
						valid = s == "tcp,udp" || s == "udp,tcp"
					}
					if !valid {
						return nil, Gateway{}, ErrGateway
					}
				}
			}
		}
	}
	current, _ = parseSource(work.current)
	mark := 0
	tags := map[string]bool{}
	for _, item := range asSlice(current.value["outbounds"]) {
		out := asMap(item)
		tag := safeString(out["tag"])
		if tag == "" || tags[tag] {
			return nil, Gateway{}, ErrGateway
		}
		tags[tag] = true
		if value, exists := asMap(asMap(out["streamSettings"])["sockopt"])["mark"]; exists {
			n, ok := markInteger(value)
			if !ok || mark != 0 && mark != n {
				return nil, Gateway{}, ErrGateway
			}
			mark = n
		}
	}
	if mark == 0 {
		v, _ := BindingValue(defaults, "xray.egress-mark")
		mark, _ = strconv.Atoi(v)
	}
	if mark == 0 {
		return nil, Gateway{}, ErrGateway
	}
	for _, value := range asSlice(factory.value["outbounds"]) {
		out := asMap(value)
		if tags[safeString(out["tag"])] {
			continue
		}
		if sock := asMap(asMap(out["streamSettings"])["sockopt"]); sock != nil {
			sock["mark"] = mark
		}
		if err := appendItem("outbounds", out); err != nil {
			return nil, Gateway{}, ErrGateway
		}
		changes = append(changes, "outbounds."+safeString(out["tag"]))
	}
	current, _ = parseSource(work.current)
	for i, item := range asSlice(current.value["outbounds"]) {
		out := asMap(item)
		if safeString(out["protocol"]) == "blackhole" {
			continue
		}
		path := []string{"outbounds", strconv.Itoa(i), "streamSettings"}
		if stream, exists := out["streamSettings"]; !exists {
			err = set(path, map[string]any{"sockopt": map[string]any{"mark": mark}})
		} else {
			object, ok := stream.(map[string]any)
			if !ok {
				return nil, Gateway{}, ErrGateway
			}
			sock, exists := object["sockopt"]
			path = append(path, "sockopt")
			if !exists {
				err = set(path, map[string]any{"mark": mark})
			} else {
				object, ok := sock.(map[string]any)
				if !ok {
					return nil, Gateway{}, ErrGateway
				}
				if _, exists := object["mark"]; !exists {
					err = set(append(path, "mark"), mark)
				}
			}
		}
		if err != nil {
			return nil, Gateway{}, ErrGateway
		}
	}
	gateway, err := InspectGateway(work.current)
	if err != nil {
		return nil, Gateway{}, err
	}
	gateway.Changes = changes
	if bytes.Equal(raw, work.current) {
		gateway.Changes = []string{}
	}
	return bytes.Clone(work.current), gateway, nil
}

// InspectGateway checks the gateway's typed Xray contract, never native rule structure.
func InspectGateway(raw []byte) (Gateway, error) {
	parsed, err := parseSource(raw)
	if err != nil {
		return Gateway{}, ErrGateway
	}
	port, err := BindingValue(raw, "xray.tproxy-port")
	if err != nil {
		return Gateway{}, ErrGateway
	}
	mark, err := BindingValue(raw, "xray.egress-mark")
	if err != nil {
		return Gateway{}, ErrGateway
	}
	result := Gateway{ProxyState: "missing", Changes: []string{}}
	result.Port, _ = strconv.Atoi(port)
	result.EgressMark, _ = strconv.Atoi(mark)
	for _, item := range asSlice(parsed.value["inbounds"]) {
		in := asMap(item)
		if safeString(in["tag"]) != "tproxy-in" {
			continue
		}
		settings := asMap(in["settings"])
		network := safeString(settings["network"])
		host := safeString(in["listen"])
		if host != "" && host != "0.0.0.0" && host != "::" {
			return Gateway{}, ErrGateway
		}
		if safeString(in["protocol"]) != "dokodemo-door" || settings["followRedirect"] != true || (network != "tcp,udp" && network != "udp,tcp") || safeString(asMap(asMap(in["streamSettings"])["sockopt"])["tproxy"]) != "tproxy" {
			return Gateway{}, ErrGateway
		}
	}
	tags := map[string]bool{}
	for _, item := range asSlice(parsed.value["outbounds"]) {
		out := asMap(item)
		tag := safeString(out["tag"])
		if tag == "" || tags[tag] {
			return Gateway{}, ErrGateway
		}
		tags[tag] = true
		protocol := safeString(out["protocol"])
		if protocol != "blackhole" {
			n, ok := markInteger(asMap(asMap(out["streamSettings"])["sockopt"])["mark"])
			if !ok || n != result.EgressMark {
				return Gateway{}, ErrGateway
			}
		}
		if tag == "proxy" {
			result.ProxyState = "configured"
			if protocol == "freedom" {
				result.ProxyState = "direct-placeholder"
			}
		}
	}
	for _, item := range asSlice(asMap(parsed.value["routing"])["rules"]) {
		tag := safeString(asMap(item)["outboundTag"])
		if tag != "" && !tags[tag] && tag != safeString(asMap(parsed.value["api"])["tag"]) {
			return Gateway{}, ErrGateway
		}
	}
	return result, nil
}
