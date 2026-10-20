// Package templatestatus aggregates read-only comparison and native
// validation results for the fixed template catalog.
package templatestatus

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatecompare"
	"home-router/xray-core-helper/internal/templatevalidation"
)

type Comparator interface {
	Compare(context.Context, templatecatalog.Entry) api.TemplateStatus
}

type Validator interface {
	Validate(context.Context, templatecatalog.ID, string) (templatevalidation.Result, error)
}

type Service struct {
	entries     []templatecatalog.Entry
	comparer    Comparator
	validator   Validator
	userGeosite string
	render      func(context.Context, templatecatalog.Entry, []byte) ([]byte, error)
}

func New(comparer Comparator, validator Validator) (*Service, error) {
	return NewWithCatalog(comparer, validator, templatecatalog.Default())
}

func NewWithCatalog(comparer Comparator, validator Validator, catalog templatecatalog.Catalog) (*Service, error) {
	if comparer == nil || validator == nil {
		return nil, errors.New("template status dependencies are unavailable")
	}
	return &Service{
		entries:   catalog.Entries(),
		comparer:  comparer,
		validator: validator,
	}, nil
}

// SetNativeRenderer prepares disposable candidates; source templates stay untouched.
func (service *Service) SetNativeRenderer(render func(context.Context, templatecatalog.Entry, []byte) ([]byte, error)) {
	service.render = render
	if comparer, ok := service.comparer.(*templatecompare.Comparer); ok {
		comparer.SetRenderer(render)
	}
}

// Refresh reads and validates every fixed template independently. It has no
// mutation, service-control or runtime-application dependency.
func (service *Service) Refresh(ctx context.Context) api.TemplateStatusResponse {
	response := api.TemplateStatusResponse{Templates: make([]api.TemplateStatus, 0, len(service.entries))}
	for _, entry := range service.entries {
		status := service.comparer.Compare(ctx, entry)
		if status.State != api.TemplateInvalid {
			status = service.validateAvailable(ctx, entry, status)
		}
		response.Templates = append(response.Templates, status)
	}
	return response
}

func (service *Service) validateAvailable(ctx context.Context, entry templatecatalog.Entry, status api.TemplateStatus) api.TemplateStatus {
	var templateErr, systemErr error
	if status.Summary.TemplateAvailable {
		templateErr = service.validateWorking(ctx, entry)
	}
	if status.Summary.SystemAvailable {
		_, systemErr = service.validator.Validate(ctx, entry.ID, entry.SystemPath)
	}
	if templateErr == nil && systemErr == nil {
		return status
	}
	status.State = api.TemplateInvalid
	status.Summary.InvalidSide = validationSide(templateErr, systemErr)
	status.Summary.Code = validationCode(templateErr, systemErr)
	return status
}

func (service *Service) validateWorking(ctx context.Context, entry templatecatalog.Entry) error {
	if service.render == nil && (entry.ID != templatecatalog.XrayConfig || service.userGeosite == "") {
		_, err := service.validator.Validate(ctx, entry.ID, entry.WorkingPath)
		return err
	}
	reader, err := templatecompare.NewSafeReader(templatecompare.MaxFileBytes)
	if err != nil {
		return err
	}
	var raw []byte
	if service.render == nil {
		raw, err = reader.BoundXray(ctx, entry.WorkingPath, service.userGeosite)
	} else {
		raw, err = reader.ReadContent(ctx, entry.WorkingPath)
		if err == nil {
			raw, err = service.render(ctx, entry, raw)
		}
	}
	if err != nil {
		return err
	}
	file, err := os.CreateTemp("", "helper-bound-template-*"+filepath.Ext(entry.SystemPath))
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	_, err = service.validator.Validate(ctx, entry.ID, file.Name())
	return err
}

func validationSide(templateErr, systemErr error) api.TemplateSide {
	if templateErr != nil && systemErr != nil {
		return api.TemplateSideBoth
	}
	if templateErr != nil {
		return api.TemplateSideTemplate
	}
	return api.TemplateSideSystem
}

func validationCode(templateErr, systemErr error) string {
	if templateErr != nil && systemErr != nil {
		return "multiple_validation_failures"
	}
	if templateErr != nil {
		return redactedValidationCode(templateErr)
	}
	return redactedValidationCode(systemErr)
}

func redactedValidationCode(err error) string {
	var failure *templatevalidation.Error
	if errors.As(err, &failure) {
		return string(failure.Code)
	}
	return string(templatevalidation.CodeValidatorUnavailable)
}

// Production constructs the read-only service with bounded filesystem and
// native-validator implementations.
func Production() (*Service, error) {
	return ProductionWithCatalog(templatecatalog.Default())
}

func ProductionWithCatalog(catalog templatecatalog.Catalog) (*Service, error) {
	return ProductionWithValidator(catalog, nil)
}
func ProductionWithValidator(catalog templatecatalog.Catalog, validator Validator, userGeosite ...string) (*Service, error) {
	comparer, err := templatecompare.New(nil, userGeosite...)
	if err != nil {
		return nil, err
	}
	if validator == nil {
		validator, err = templatevalidation.New(nil)
		if err != nil {
			return nil, err
		}
	}
	service, err := NewWithCatalog(comparer, validator, catalog)
	if err == nil && len(userGeosite) > 0 {
		service.userGeosite = userGeosite[0]
	}
	return service, err
}
