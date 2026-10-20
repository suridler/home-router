package templatestatus

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatevalidation"
)

type fakeComparator struct {
	statuses map[templatecatalog.ID]api.TemplateStatus
	calls    []templatecatalog.ID
}

func (comparer *fakeComparator) Compare(_ context.Context, entry templatecatalog.Entry) api.TemplateStatus {
	comparer.calls = append(comparer.calls, entry.ID)
	if status, ok := comparer.statuses[entry.ID]; ok {
		return status
	}
	return synchronized(entry.ID)
}

type validationCall struct {
	id   templatecatalog.ID
	path string
}

type fakeValidator struct {
	fail  map[string]error
	calls []validationCall
}

func TestRefreshValidatesServiceConfigPath(t *testing.T) {
	catalog, err := templatecatalog.ForXrayConfig("/etc/xray/service-selected.json")
	if err != nil {
		t.Fatal(err)
	}
	validator := &fakeValidator{}
	service, err := NewWithCatalog(&fakeComparator{}, validator, catalog)
	if err != nil {
		t.Fatal(err)
	}
	service.Refresh(context.Background())
	found := false
	for _, call := range validator.calls {
		if call.id == templatecatalog.XrayConfig && call.path == "/etc/xray/service-selected.json" {
			found = true
		}
		if call.path == "/usr/local/etc/xray/config.json" {
			t.Fatal("validated hardcoded config")
		}
	}
	if !found {
		t.Fatal("service config was not validated")
	}
}

func (validator *fakeValidator) Validate(_ context.Context, id templatecatalog.ID, path string) (templatevalidation.Result, error) {
	validator.calls = append(validator.calls, validationCall{id: id, path: path})
	if err := validator.fail[path]; err != nil {
		return templatevalidation.Result{}, err
	}
	return templatevalidation.Result{ID: id, Status: "valid"}, nil
}

func TestRefreshPreservesCatalogOrderAndContinuesAfterBadTemplate(t *testing.T) {
	entries := templatecatalog.Default().Entries()
	bad := entries[1]
	comparer := &fakeComparator{statuses: map[templatecatalog.ID]api.TemplateStatus{
		bad.ID: invalidRead(bad.ID),
	}}
	validator := &fakeValidator{fail: map[string]error{}}
	service, err := New(comparer, validator)
	if err != nil {
		t.Fatal(err)
	}

	response := service.Refresh(context.Background())
	if len(response.Templates) != len(entries) {
		t.Fatalf("templates = %d; want %d", len(response.Templates), len(entries))
	}
	wantOrder := make([]templatecatalog.ID, 0, len(entries))
	for index, entry := range entries {
		wantOrder = append(wantOrder, entry.ID)
		if response.Templates[index].TemplateID != string(entry.ID) {
			t.Errorf("item %d ID = %q; want %q", index, response.Templates[index].TemplateID, entry.ID)
		}
		if err := response.Templates[index].Validate(); err != nil {
			t.Errorf("item %d invalid: %v", index, err)
		}
	}
	if !reflect.DeepEqual(comparer.calls, wantOrder) {
		t.Fatalf("compare order = %v; want %v", comparer.calls, wantOrder)
	}
	if response.Templates[1].State != api.TemplateInvalid {
		t.Fatalf("bad template state = %q", response.Templates[1].State)
	}
	if response.Templates[2].State != api.TemplateSynchronized {
		t.Fatal("bad template hid a later result")
	}
	for _, call := range validator.calls {
		if call.id == bad.ID {
			t.Fatal("unsafe comparison result was passed to native validator")
		}
	}
}

func TestRefreshValidatesOnlyAvailableSides(t *testing.T) {
	entries := templatecatalog.Default().Entries()
	missingTemplate := entries[0]
	missingSystem := entries[1]
	comparer := &fakeComparator{statuses: map[templatecatalog.ID]api.TemplateStatus{
		missingTemplate.ID: missingWorking(missingTemplate.ID),
		missingSystem.ID:   missingSystemFile(missingSystem.ID),
	}}
	validator := &fakeValidator{fail: map[string]error{}}
	service, _ := New(comparer, validator)
	service.Refresh(context.Background())

	for _, call := range validator.calls {
		if call.path == missingTemplate.WorkingPath || call.path == missingSystem.SystemPath {
			t.Fatalf("validator called for missing side: %#v", call)
		}
	}
	assertValidationCall(t, validator.calls, missingTemplate.ID, missingTemplate.SystemPath)
	assertValidationCall(t, validator.calls, missingSystem.ID, missingSystem.WorkingPath)
}

func TestRefreshProjectsNativeValidationFailuresPerTemplate(t *testing.T) {
	entries := templatecatalog.Default().Entries()
	target := entries[2]
	secretFailure := errors.New("secret unit contents and command output")
	validator := &fakeValidator{fail: map[string]error{
		target.WorkingPath: &templatevalidation.Error{Code: templatevalidation.CodeInvalidSyntax},
		target.SystemPath:  secretFailure,
	}}
	service, _ := New(&fakeComparator{statuses: map[templatecatalog.ID]api.TemplateStatus{}}, validator)
	response := service.Refresh(context.Background())
	status := response.Templates[2]
	if status.State != api.TemplateInvalid || status.Summary.InvalidSide != api.TemplateSideBoth || status.Summary.Code != "multiple_validation_failures" {
		t.Fatalf("status = %#v", status)
	}
	if strings.Contains(status.Summary.Code, "secret") || strings.Contains(status.Summary.Code, "command") {
		t.Fatalf("status leaked validator failure: %#v", status)
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("invalid projected contract: %v", err)
	}
	if response.Templates[3].State != api.TemplateSynchronized {
		t.Fatal("validation failure hid a later template")
	}
}

func TestRefreshHasOnlyReadAndValidateDependencies(t *testing.T) {
	comparer := &fakeComparator{statuses: map[templatecatalog.ID]api.TemplateStatus{}}
	validator := &fakeValidator{fail: map[string]error{}}
	service, err := New(comparer, validator)
	if err != nil {
		t.Fatal(err)
	}
	service.Refresh(context.Background())
	want := len(templatecatalog.Default().Entries())
	if len(comparer.calls) != want || len(validator.calls) != want*2 {
		t.Fatalf("calls compare=%d validate=%d; want %d and %d", len(comparer.calls), len(validator.calls), want, want*2)
	}
}

func synchronized(id templatecatalog.ID) api.TemplateStatus {
	hash := strings.Repeat("a", 64)
	return api.TemplateStatus{
		TemplateID: string(id), State: api.TemplateSynchronized,
		TemplateHash: hash, SystemHash: hash, Revision: strings.Repeat("b", 64),
		Summary: api.TemplateStatusSummary{TemplateAvailable: true, SystemAvailable: true},
	}
}

func invalidRead(id templatecatalog.ID) api.TemplateStatus {
	return api.TemplateStatus{
		TemplateID: string(id), State: api.TemplateInvalid, Revision: strings.Repeat("b", 64),
		Summary: api.TemplateStatusSummary{TemplateAvailable: true, SystemAvailable: true, InvalidSide: api.TemplateSideTemplate, Code: "unsafe_file"},
	}
}

func missingWorking(id templatecatalog.ID) api.TemplateStatus {
	return api.TemplateStatus{
		TemplateID: string(id), State: api.TemplateMissing, SystemHash: strings.Repeat("a", 64), Revision: strings.Repeat("b", 64),
		Summary: api.TemplateStatusSummary{SystemAvailable: true},
	}
}

func missingSystemFile(id templatecatalog.ID) api.TemplateStatus {
	return api.TemplateStatus{
		TemplateID: string(id), State: api.TemplateSystemMissing, TemplateHash: strings.Repeat("a", 64), Revision: strings.Repeat("b", 64),
		Summary: api.TemplateStatusSummary{TemplateAvailable: true},
	}
}

func assertValidationCall(t *testing.T, calls []validationCall, id templatecatalog.ID, path string) {
	t.Helper()
	for _, call := range calls {
		if call.id == id && call.path == path {
			return
		}
	}
	t.Fatalf("missing validation call for %q", id)
}
