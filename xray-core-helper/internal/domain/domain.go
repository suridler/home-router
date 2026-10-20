package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrConsolidationActive = errors.New("proxy-domain consolidation is active")

func Normalize(value string) (string, error) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if len(name) == 0 || len(name) > 253 {
		return "", fmt.Errorf("invalid DNS domain length")
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("domain must contain at least two labels")
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid DNS label %q", label)
		}
		for _, char := range label {
			if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
				continue
			}
			return "", fmt.Errorf("invalid character in DNS label %q", label)
		}
	}
	return name, nil
}

func NormalizeList(values []string) ([]string, error) {
	normalized := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		name, err := Normalize(value)
		if err != nil {
			return nil, err
		}
		normalized[name] = struct{}{}
	}
	result := make([]string, 0, len(normalized))
	for name := range normalized {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}
