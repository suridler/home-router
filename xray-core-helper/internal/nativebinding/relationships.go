package nativebinding

import (
	"sort"
	"strings"
)

// Check is a secret-free result for one explicit declaration.
type Check struct {
	TemplateID string
	BindingID  string
	Status     string
}

// CheckDocuments follows only declared references. It never reads files or
// discovers service/network state on its own. Callers supply private snapshots.
func CheckDocuments(documents map[string]*Document, external Resolver) ([]Check, error) {
	keys := make([]string, 0, len(documents))
	for key, doc := range documents {
		if doc == nil {
			return nil, ErrInvalid
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var results []Check
	for _, key := range keys {
		for _, binding := range documents[key].bindings {
			// Setup inputs authorize rendering, not a permanent relationship to
			// a rediscovered host interface/subnet after installation.
			if strings.HasPrefix(binding.Source, "setup.") {
				results = append(results, Check{TemplateID: key, BindingID: binding.ID, Status: "setup_only"})
				continue
			}
			value, err := resolveBinding(key, binding, documents, external, map[string]bool{}, 0)
			status := "ok"
			if err == ErrInvalid || err == ErrLimit {
				return nil, err
			}
			if err != nil {
				status = "unresolved"
			} else {
				actual, _ := normalize(binding.Type, binding.value)
				if actual != value {
					status = "mismatch"
				}
			}
			results = append(results, Check{TemplateID: key, BindingID: binding.ID, Status: status})
		}
	}
	return results, nil
}

func resolveBinding(template string, binding Binding, docs map[string]*Document, external Resolver, active map[string]bool, depth int) (string, error) {
	if depth > 64 {
		return "", ErrLimit
	}
	key := template + "." + binding.ID
	if active[key] {
		return "", ErrInvalid
	}
	active[key] = true
	defer delete(active, key)
	if binding.Source == "native.literal" || strings.HasPrefix(binding.Source, "setup.") {
		return normalize(binding.Type, binding.value)
	}
	if strings.HasPrefix(binding.Source, "native.") {
		parts := strings.Split(binding.Source, ".")
		doc := docs[parts[1]]
		if doc == nil {
			return "", ErrUnresolved
		}
		for _, target := range doc.bindings {
			if target.ID != parts[2] {
				continue
			}
			compatible := target.Type == binding.Type || (target.Type == "uint32" || target.Type == "port") && (binding.Type == "uint32" || binding.Type == "port")
			if !compatible {
				return "", ErrInvalid
			}
			// Traverse to detect cycles/unresolved dependencies, but equality is
			// with the actual explicitly marked system value, not a substituted
			// external value. The target has its own independent check result.
			if _, err := resolveBinding(parts[1], target, docs, external, active, depth+1); err != nil {
				return "", err
			}
			value, err := normalize(binding.Type, target.value)
			if err != nil {
				return "", ErrUnresolved
			}
			return value, nil
		}
		return "", ErrUnresolved
	}
	if external == nil {
		return "", ErrUnresolved
	}
	value, err := external(binding.Source)
	if err != nil {
		return "", ErrUnresolved
	}
	normalized, err := normalize(binding.Type, value)
	if err != nil {
		return "", ErrUnresolved
	}
	return normalized, nil
}
