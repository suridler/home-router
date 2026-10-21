package bootstrap

import (
	"bytes"
	"fmt"
	"reflect"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// Decode overlays explicitly supplied TOML values on the release defaults.
// In particular, false, zero and empty strings/arrays are not missing values.
func Decode(raw []byte, base Config) (Config, error) {
	config, err := parseDefaults(base.defaults)
	if err != nil {
		return Config{}, err
	}
	decoder := toml.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode helper config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// UpdateOverrides replaces only the named sections with their differences from
// the release defaults. Other sections and their comments remain unchanged.
// A value equal to its default removes the corresponding override.
func UpdateOverrides(raw []byte, next Config, sections ...string) ([]byte, error) {
	if _, err := Decode(raw, next); err != nil {
		return nil, err
	}
	if err := next.Validate(); err != nil {
		return nil, err
	}
	values, err := configMap(next)
	if err != nil {
		return nil, err
	}
	defaultConfig, err := parseDefaults(next.defaults)
	if err != nil {
		return nil, err
	}
	defaults, err := configMap(defaultConfig)
	if err != nil {
		return nil, err
	}
	owned := make(map[string]bool, len(sections))
	patch := make(map[string]any)
	for _, section := range sections {
		value, ok := values[section].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unknown helper config section: %s", section)
		}
		owned[section] = true
		diff := differentValues(value, defaults[section].(map[string]any))
		if len(diff) != 0 {
			patch[section] = diff
		}
	}
	kept, err := withoutSections(raw, owned)
	if err != nil {
		return nil, err
	}
	encoded, err := toml.Marshal(patch)
	if err != nil {
		return nil, err
	}
	result := kept
	if len(encoded) > 0 {
		result = append(bytes.TrimRight(kept, "\n"), '\n', '\n')
		result = append(result, encoded...)
	}
	if _, err := Decode(result, next); err != nil {
		return nil, err
	}
	return result, nil
}

func configMap(config Config) (map[string]any, error) {
	raw, err := toml.Marshal(config)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	err = toml.Unmarshal(raw, &values)
	return values, err
}

func differentValues(values, defaults map[string]any) map[string]any {
	result := make(map[string]any)
	for key, value := range values {
		if table, ok := value.(map[string]any); ok {
			diff := differentValues(table, defaults[key].(map[string]any))
			if len(diff) > 0 {
				result[key] = diff
			}
		} else if !reflect.DeepEqual(value, defaults[key]) {
			result[key] = value
		}
	}
	return result
}

// Use TOML expression boundaries so quoted keys, dotted keys, inline tables,
// multiline values and comments are handled without line-based heuristics.
func withoutSections(raw []byte, ownedSections map[string]bool) ([]byte, error) {
	type segment struct {
		offset int
		drop   bool
	}
	var parser unstable.Parser
	parser.KeepComments = true
	parser.Reset(raw)
	segments := []segment{}
	inTable, owned := false, false
	for parser.NextExpression() {
		node := parser.Expression()
		offset, drop := int(node.Raw.Offset), false
		switch node.Kind {
		case unstable.Table, unstable.ArrayTable, unstable.KeyValue:
			keys := node.Key()
			if !keys.Next() {
				return nil, fmt.Errorf("invalid helper TOML key")
			}
			key := keys.Node()
			offset = int(key.Raw.Offset)
			ownKey := ownedSections[string(key.Data)]
			if node.Kind != unstable.KeyValue {
				inTable = true
				owned = ownKey
			}
			drop = owned || (!inTable && ownKey)
		case unstable.Comment:
		default:
			return nil, fmt.Errorf("invalid helper TOML expression")
		}
		start := bytes.LastIndexByte(raw[:offset], '\n') + 1
		// An inline comment belongs to the preceding value, not to a new line.
		if len(segments) > 0 && start == segments[len(segments)-1].offset {
			continue
		}
		segments = append(segments, segment{start, drop})
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		return append([]byte(nil), raw...), nil
	}
	var kept bytes.Buffer
	kept.Write(raw[:segments[0].offset])
	for i, segment := range segments {
		end := len(raw)
		if i+1 < len(segments) {
			end = segments[i+1].offset
		}
		if !segment.drop {
			kept.Write(raw[segment.offset:end])
		}
	}
	return kept.Bytes(), nil
}
