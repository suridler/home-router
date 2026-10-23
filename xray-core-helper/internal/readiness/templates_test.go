package readiness

import (
	"context"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
)

type templatesFake struct {
	response api.TemplateStatusResponse
	ready    bool
}

func (fake templatesFake) Current() (api.TemplateStatusResponse, bool) {
	return fake.response, fake.ready
}

func TestTemplateReadinessProjection(t *testing.T) {
	hash := strings.Repeat("a", 64)
	revision := strings.Repeat("b", 64)
	item := api.TemplateStatus{
		TemplateID: "xray-config", State: api.TemplateSynchronized,
		TemplateHash: hash, SystemHash: hash, Revision: revision,
		Summary: api.TemplateStatusSummary{TemplateAvailable: true, SystemAvailable: true},
	}
	tests := []struct {
		name      string
		templates TemplateStatus
		code      string
		status    string
	}{
		{name: "unavailable", code: "template_status_unavailable", status: StatusDegraded},
		{name: "not initialized", templates: templatesFake{}, code: "template_status_unavailable", status: StatusDegraded},
		{name: "synchronized", templates: templatesFake{response: api.TemplateStatusResponse{Templates: []api.TemplateStatus{item}}, ready: true}, code: "templates_synchronized", status: StatusReady},
		{name: "different", templates: templatesFake{response: api.TemplateStatusResponse{Templates: []api.TemplateStatus{withState(item, api.TemplateDifferent)}}, ready: true}, code: "templates_available", status: StatusReady},
		{name: "missing", templates: templatesFake{response: api.TemplateStatusResponse{Templates: []api.TemplateStatus{withState(item, api.TemplateMissing)}}, ready: true}, code: "templates_different", status: StatusDegraded},
		{name: "system missing", templates: templatesFake{response: api.TemplateStatusResponse{Templates: []api.TemplateStatus{withState(item, api.TemplateSystemMissing)}}, ready: true}, code: "templates_different", status: StatusDegraded},
		{name: "invalid", templates: templatesFake{response: api.TemplateStatusResponse{Templates: []api.TemplateStatus{withState(item, api.TemplateInvalid)}}, ready: true}, code: "templates_invalid", status: StatusDegraded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, database, xrayAPI, recovery := healthyDependencies()
			var service *Service
			if test.templates == nil {
				service = New(config, database, xrayAPI, recovery)
			} else {
				service = New(config, database, xrayAPI, recovery, test.templates)
			}
			result := service.Check(context.Background())
			if result.Status != test.status {
				t.Fatalf("status = %q; want %q", result.Status, test.status)
			}
			last := result.Checks[len(result.Checks)-1]
			if last.Name != "templates" || last.Code != test.code || last.Status != test.status {
				t.Fatalf("templates check = %#v", last)
			}
		})
	}
}

func withState(item api.TemplateStatus, state api.TemplateState) api.TemplateStatus {
	item.State = state
	return item
}

func TestDifferentTemplatesDoNotHideMissingOrInvalidFiles(t *testing.T) {
	for _, state := range []api.TemplateState{api.TemplateMissing, api.TemplateSystemMissing, api.TemplateInvalid} {
		for _, reverse := range []bool{false, true} {
			items := []api.TemplateStatus{{TemplateID: "xray-config", State: api.TemplateDifferent}, {TemplateID: "nftables", State: state}}
			if reverse {
				items[0], items[1] = items[1], items[0]
			}
			service := &Service{templates: templatesFake{response: api.TemplateStatusResponse{Templates: items}, ready: true}}
			check := service.checkTemplates()
			if check.Status != StatusDegraded {
				t.Fatalf("%s reverse=%v: %#v", state, reverse, check)
			}
		}
	}
	items := []api.TemplateStatus{}
	for _, id := range []string{"xray-config", "nftables", "tproxy-unit", "sysctl"} {
		items = append(items, api.TemplateStatus{TemplateID: id, State: api.TemplateDifferent})
	}
	fake := templatesFake{response: api.TemplateStatusResponse{Templates: items}, ready: true}
	service := &Service{templates: fake}
	if check := service.checkTemplates(); check.Status != StatusReady || check.Code != "templates_available" {
		t.Fatalf("%#v", check)
	}
	status, _ := fake.Current()
	for _, item := range status.Templates {
		if item.State != api.TemplateDifferent {
			t.Fatal("comparison status changed")
		}
	}
}
