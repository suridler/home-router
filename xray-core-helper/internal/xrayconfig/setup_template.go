package xrayconfig

import (
	"bytes"
	"encoding/json"
)

// RenderSetupTemplate supplies initial gateway defaults only for fields absent
// from the selected config. Invalid or ambiguous existing values are never
// treated as missing. Ordinary template apply uses RenderTemplate instead.
func RenderSetupTemplate(raw, current []byte, values TemplateValues) ([]byte, error) {
	port, mark := 52345, 255
	if len(bytes.TrimSpace(current)) != 0 {
		parsed, err := parseSource(current)
		if err != nil {
			return nil, ErrGateway
		}
		for _, section := range []string{"inbounds", "outbounds"} {
			if value, exists := parsed.value[section]; exists {
				if _, ok := value.([]any); !ok {
					return nil, ErrGateway
				}
			}
		}
		seen := false
		for _, item := range asSlice(parsed.value["inbounds"]) {
			in, ok := item.(map[string]any)
			if !ok {
				return nil, ErrGateway
			}
			if safeString(in["tag"]) != "tproxy-in" {
				continue
			}
			if seen {
				return nil, ErrGateway
			}
			seen = true
			if value, exists := in["port"]; exists {
				var valid bool
				port, valid = relationInteger(value)
				if !valid {
					return nil, ErrGateway
				}
			}
		}
		seen = false
		for _, item := range asSlice(parsed.value["outbounds"]) {
			out, ok := item.(map[string]any)
			if !ok {
				return nil, ErrGateway
			}
			// A present malformed object is not permission to infer a default.
			stream, present := out["streamSettings"]
			if present {
				if _, ok := stream.(map[string]any); !ok {
					return nil, ErrGateway
				}
			}
			sockopt, present := asMap(stream)["sockopt"]
			if present {
				if _, ok := sockopt.(map[string]any); !ok {
					return nil, ErrGateway
				}
			}
			if value, exists := asMap(sockopt)["mark"]; exists {
				selected, valid := markInteger(value)
				if !valid || seen && selected != mark {
					return nil, ErrGateway
				}
				mark, seen = selected, true
			}
		}
	}
	// The projection contains only the two selected scalars, never proxy secrets.
	values.Xray, _ = json.Marshal(map[string]any{
		"inbounds":  []any{map[string]any{"tag": "tproxy-in", "port": port}},
		"outbounds": []any{map[string]any{"streamSettings": map[string]any{"sockopt": map[string]any{"mark": mark}}}},
	})
	return RenderTemplate(raw, values)
}
