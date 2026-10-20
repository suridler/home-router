package xrayconfig

import (
	"fmt"
	"home-router/xray-core-helper/internal/bootstrap"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/transaction"
)

const (
	managedAccessLogPath = "/var/log/xray/access.log"
	managedErrorLogPath  = "/var/log/xray/error.log"
)

var allowedLogLevels = map[string]struct{}{
	"debug":   {},
	"info":    {},
	"warning": {},
	"error":   {},
	"none":    {},
}

// ValidateLogConfigurationRequest validates the transport-neutral logging contract.
func ValidateLogConfigurationRequest(request api.LogConfigurationRequest) error {
	for _, target := range []struct {
		name  string
		value api.LogTarget
	}{
		{name: "access", value: request.Access},
		{name: "error", value: request.Error},
	} {
		switch target.value.Mode {
		case api.LogTargetDisabled, api.LogTargetJournal, api.LogTargetFile:
		default:
			return transaction.Reject("invalid_log_mode", fmt.Sprintf("%s log mode must be disabled, journal, or file", target.name), 400)
		}
	}
	if _, ok := allowedLogLevels[request.Loglevel]; !ok {
		return transaction.Reject("invalid_loglevel", "loglevel must be debug, info, warning, error, or none", 400)
	}
	if request.DNSLog == nil {
		return transaction.Reject("invalid_dns_log", "dnsLog must be provided as a boolean", 400)
	}
	return nil
}

// PlanLogConfiguration edits only the managed members of the existing Xray log object.
func PlanLogConfiguration(request api.LogConfigurationRequest) transaction.Planner {
	return PlanLogConfigurationValues(request, bootstrap.LoggingConfig{AccessPath: managedAccessLogPath, ErrorPath: managedErrorLogPath})
}

// ResolveLogConfiguration preserves an operator-configured file path. Journal
// and disabled values have no remembered file location and use the fixed default.
func ResolveLogConfiguration(request api.LogConfigurationRequest, current bootstrap.LoggingConfig) (bootstrap.LoggingConfig, error) {
	if err := ValidateLogConfigurationRequest(request); err != nil {
		return bootstrap.LoggingConfig{}, err
	}
	file := func(value, fallback string) string {
		if value == "" || value == "none" {
			return fallback
		}
		return value
	}
	next := bootstrap.LoggingConfig{
		AccessPath: logTargetPath(request.Access.Mode, file(current.AccessPath, managedAccessLogPath)),
		ErrorPath:  logTargetPath(request.Error.Mode, file(current.ErrorPath, managedErrorLogPath)),
		Level:      request.Loglevel, DNSLog: *request.DNSLog,
	}
	if next.Validate() != nil {
		return bootstrap.LoggingConfig{}, transaction.Reject("invalid_log_path", "configured logging path is invalid", 400)
	}
	return next, nil
}

// PlanLogConfigurationValues applies resolved helper values to the four typed
// Xray members, preserving comments and unknown fields.
func PlanLogConfigurationValues(request api.LogConfigurationRequest, values bootstrap.LoggingConfig) transaction.Planner {
	if request.DNSLog != nil {
		dnsLog := *request.DNSLog
		request.DNSLog = &dnsLog
	}
	return func(source []byte) ([]transaction.ByteEdit, error) {
		if err := ValidateLogConfigurationRequest(request); err != nil {
			return nil, err
		}
		resolved, err := ResolveLogConfiguration(request, values)
		if err != nil {
			return nil, err
		}
		e, err := newEditor(source)
		if err != nil {
			return nil, transaction.Reject("invalid_config", "Xray configuration contains invalid JSONC", 422)
		}
		logNode, err := logConfigurationObject(e.syntax)
		if err != nil {
			return nil, err
		}
		if hasMissingLogMembers(logNode) && hasComments(e, logNode) {
			return nil, transaction.Reject("logging_comment_ambiguous", "logging members cannot be added safely to a commented log object", 409)
		}

		dnsLog := *request.DNSLog
		desired := []struct {
			name  string
			value any
		}{
			{name: "access", value: resolved.AccessPath},
			{name: "error", value: resolved.ErrorPath},
			{name: "loglevel", value: request.Loglevel},
			{name: "dnsLog", value: dnsLog},
		}
		work := newSession(source)
		for _, setting := range desired {
			err := work.apply(func(step *editor) ([]transaction.ByteEdit, error) {
				object, err := logConfigurationObject(step.syntax)
				if err != nil {
					return nil, err
				}
				if node := object.object[setting.name]; node != nil {
					if jsonValueEqual(step.syntax.value["log"].(map[string]any)[setting.name], setting.value) {
						return nil, nil
					}
					edit, err := step.replaceNode(node, setting.value)
					if err != nil {
						return nil, err
					}
					return []transaction.ByteEdit{edit}, nil
				}
				edit, err := step.appendObjectMember(object, setting.name, setting.value)
				if err != nil {
					return nil, err
				}
				return []transaction.ByteEdit{edit}, nil
			})
			if err != nil {
				return nil, err
			}
		}
		return work.edits(), nil
	}
}

func logTargetPath(mode api.LogTargetMode, filePath string) string {
	switch mode {
	case api.LogTargetDisabled:
		return "none"
	case api.LogTargetJournal:
		return ""
	case api.LogTargetFile:
		return filePath
	default:
		return ""
	}
}

func logConfigurationObject(syntax *parsedSource) (*jsonNode, error) {
	var rootLog *jsonNode
	rootLogCount := 0
	for _, member := range syntax.spans.members {
		if member.key == "log" {
			rootLog = member.value
			rootLogCount++
		}
	}
	if rootLogCount != 1 || rootLog == nil || rootLog.object == nil {
		return nil, transaction.Reject("invalid_config", "Xray configuration must contain one log object", 422)
	}
	logging, ok := syntax.value["log"].(map[string]any)
	if !ok {
		return nil, transaction.Reject("invalid_config", "Xray configuration must contain one log object", 422)
	}

	for _, name := range []string{"access", "error", "loglevel", "dnsLog"} {
		count := 0
		for _, member := range rootLog.members {
			if member.key == name {
				count++
			}
		}
		if count > 1 {
			return nil, transaction.Reject("invalid_config", "Xray log object contains an ambiguous managed member", 422)
		}
		if count == 0 {
			continue
		}
		value := logging[name]
		switch name {
		case "access", "error", "loglevel":
			if _, ok := value.(string); !ok {
				return nil, transaction.Reject("invalid_config", "Xray log object contains a managed member with the wrong type", 422)
			}
		case "dnsLog":
			if _, ok := value.(bool); !ok {
				return nil, transaction.Reject("invalid_config", "Xray log object contains a managed member with the wrong type", 422)
			}
		}
	}
	return rootLog, nil
}

func hasMissingLogMembers(object *jsonNode) bool {
	for _, name := range []string{"access", "error", "loglevel", "dnsLog"} {
		if object.object[name] == nil {
			return true
		}
	}
	return false
}

func jsonValueEqual(left, right any) bool {
	switch expected := right.(type) {
	case string:
		actual, ok := left.(string)
		return ok && actual == expected
	case bool:
		actual, ok := left.(bool)
		return ok && actual == expected
	default:
		return false
	}
}
