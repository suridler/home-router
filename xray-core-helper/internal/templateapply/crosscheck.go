// Package templateapply contains the internal Template -> System apply
// foundations. Public callers remain safety-gated until confirmed apply exists.
package templateapply

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/nativebinding"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatevalidation"
	"home-router/xray-core-helper/internal/xrayconfig"
	"home-router/xray-core-helper/internal/xrayidentity"
)

type Code string

const (
	CodeUnknownTemplate   Code = "unknown_template"
	CodeValidation        Code = "template_validation_failed"
	CodeRelatedRead       Code = "related_native_file_unavailable"
	CodeBackup            Code = "system_backup_failed"
	CodeReplace           Code = "system_replace_failed"
	CodeRuntime           Code = "runtime_apply_failed"
	CodeHealth            Code = "runtime_health_failed"
	CodeXrayEffect        Code = "xray_api_effect_failed"
	CodeRollback          Code = "system_rollback_failed"
	CodeSafeApply         Code = "safe_apply_required"
	CodeGeneration        Code = "template_generation_conflict"
	CodeInitialize        Code = "initialization_required"
	CodeBindingInvalid    Code = "binding_invalid"
	CodeBindingUnresolved Code = "binding_unresolved"
	CodeBindingMismatch   Code = "binding_mismatch"
)

type Error struct{ Code Code }

func (failure *Error) Error() string { return string(failure.Code) }
func IsCode(err error, code Code) bool {
	var failure *Error
	return errors.As(err, &failure) && failure.Code == code
}

type Validator interface {
	Validate(context.Context, templatecatalog.ID, string) (templatevalidation.Result, error)
}

type CrossChecker struct {
	network       bootstrap.NetworkConfig
	userGeosite   string
	xrayValues    *xrayconfig.TemplateValues
	loggingValues func() bootstrap.LoggingConfig
	identity      func(context.Context) (int, error)
	catalog       templatecatalog.Catalog
	validator     Validator
	read          func(string) ([]byte, error)
}

func NewCrossChecker(validator Validator) (*CrossChecker, error) {
	return NewCrossCheckerWithCatalog(validator, templatecatalog.Default())
}

func NewCrossCheckerWithCatalog(validator Validator, catalog templatecatalog.Catalog, userGeosite ...string) (*CrossChecker, error) {
	if validator == nil {
		return nil, &Error{Code: CodeValidation}
	}
	name := bootstrap.DefaultUserGeositeName
	if len(userGeosite) > 0 {
		name = userGeosite[0]
	}
	if !bootstrap.ValidUserGeositeName(name) {
		return nil, &Error{Code: CodeBindingInvalid}
	}
	return &CrossChecker{network: bootstrap.Defaults().Network, userGeosite: name, catalog: catalog, validator: validator, identity: xrayidentity.New().GID, read: func(path string) ([]byte, error) { content, _, _, _, _, err := readRegular(path); return content, err }}, nil
}

// Check validates native syntax and only explicitly declared relationships.
func (checker *CrossChecker) Check(ctx context.Context, id templatecatalog.ID, candidate string) error {
	if _, err := checker.catalog.Lookup(id); err != nil {
		return &Error{Code: CodeUnknownTemplate}
	}
	if _, err := checker.validator.Validate(ctx, id, candidate); err != nil {
		return &Error{Code: CodeValidation}
	}
	raw, err := checker.read(candidate)
	if err != nil {
		return &Error{Code: CodeRelatedRead}
	}
	documents := map[string]*nativebinding.Document{}
	var xray []byte
	if id == templatecatalog.XrayConfig {
		xray = raw
		if !bytes.Contains(raw, []byte("// helper:")) {
			return nil
		}
		doc, err := nativebinding.Parse(raw)
		if err != nil {
			return &Error{Code: CodeBindingInvalid}
		}
		documents[string(id)] = doc
	} else {
		doc, err := nativebinding.Parse(raw)
		if err != nil {
			return &Error{Code: CodeBindingInvalid}
		}
		if len(doc.Bindings()) == 0 {
			return nil
		}
		documents[string(id)] = doc
	}
	for _, related := range []templatecatalog.ID{templatecatalog.Nftables, templatecatalog.TProxyUnit, templatecatalog.Sysctl} {
		if related == id {
			continue
		}
		entry, _ := checker.catalog.Lookup(related)
		value, readErr := checker.read(entry.SystemPath)
		if readErr != nil {
			continue
		} // only an explicit reference requires this file
		doc, parseErr := nativebinding.Parse(value)
		if parseErr != nil {
			return &Error{Code: CodeBindingInvalid}
		}
		documents[string(related)] = doc
	}
	checks, err := nativebinding.CheckDocuments(documents, func(provider string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if strings.HasPrefix(provider, "helper.network.") {
			return checker.network.BindingValue(provider)
		}
		if checker.xrayValues != nil && strings.HasPrefix(provider, "helper.") {
			values := *checker.xrayValues
			if checker.loggingValues != nil {
				values.Logging = checker.loggingValues()
			}
			return values.Resolve(provider)
		}
		if provider == "helper.user-geosite" {
			return checker.userGeosite, nil
		}
		if provider == "xray.service-gid" {
			if checker.identity == nil {
				return "", nativebinding.ErrUnresolved
			}
			gid, err := checker.identity(ctx)
			return strconv.Itoa(gid), err
		}
		if strings.HasPrefix(provider, "xray.") {
			if xray == nil {
				entry, _ := checker.catalog.Lookup(templatecatalog.XrayConfig)
				var err error
				xray, err = checker.read(entry.SystemPath)
				if err != nil {
					return "", nativebinding.ErrUnresolved
				}
			}
			return xrayconfig.BindingValue(xray, provider)
		}
		return "", nativebinding.ErrUnresolved
	})
	if err != nil {
		return &Error{Code: CodeBindingInvalid}
	}
	for _, check := range checks {
		if check.Status == "unresolved" {
			return &Error{Code: CodeBindingUnresolved}
		}
		if check.Status == "mismatch" {
			return &Error{Code: CodeBindingMismatch}
		}
	}
	return nil
}
