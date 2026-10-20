package templateprojection

import (
	"context"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/templatecatalog"
)

type refresherFake struct {
	response api.TemplateStatusResponse
	calls    int
}

func (fake *refresherFake) Refresh(context.Context) api.TemplateStatusResponse {
	fake.calls++
	return fake.response
}

func TestInitializeStoresDefensiveStartupSnapshot(t *testing.T) {
	fake := &refresherFake{response: responseWith(api.TemplateSynchronized)}
	manager, err := New(fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Current(); ok {
		t.Fatal("projection available before Initialize")
	}
	startup := manager.Initialize(context.Background())
	if fake.calls != 1 || len(startup.Templates) != 1 {
		t.Fatalf("startup=%#v calls=%d", startup, fake.calls)
	}
	startup.Templates[0].State = api.TemplateInvalid
	current, ok := manager.Current()
	if !ok || current.Templates[0].State != api.TemplateSynchronized {
		t.Fatalf("stored projection mutated through result: %#v", current)
	}
	current.Templates[0].State = api.TemplateInvalid
	again, _ := manager.Current()
	if again.Templates[0].State != api.TemplateSynchronized {
		t.Fatal("stored projection mutated through Current")
	}
}

func TestRequireSynchronized(t *testing.T) {
	revision := strings.Repeat("b", 64)
	tests := []struct {
		name     string
		state    api.TemplateState
		id       templatecatalog.ID
		expected string
		init     bool
		code     Code
	}{
		{name: "allowed", state: api.TemplateSynchronized, id: templatecatalog.XrayConfig, expected: revision, init: true},
		{name: "different", state: api.TemplateDifferent, id: templatecatalog.XrayConfig, expected: revision, init: true, code: CodeSynchronizationRequired},
		{name: "missing", state: api.TemplateMissing, id: templatecatalog.XrayConfig, expected: revision, init: true, code: CodeSynchronizationRequired},
		{name: "invalid", state: api.TemplateInvalid, id: templatecatalog.XrayConfig, expected: revision, init: true, code: CodeSynchronizationRequired},
		{name: "stale", state: api.TemplateSynchronized, id: templatecatalog.XrayConfig, expected: strings.Repeat("c", 64), init: true, code: CodeRevisionConflict},
		{name: "unknown", state: api.TemplateSynchronized, id: "private-path", expected: revision, init: true, code: CodeUnknownTemplate},
		{name: "not initialized", state: api.TemplateSynchronized, id: templatecatalog.XrayConfig, expected: revision, code: CodeStatusUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &refresherFake{response: responseWith(test.state)}
			manager, _ := New(fake)
			if test.init {
				manager.Initialize(context.Background())
			}
			err := manager.RequireSynchronized(test.id, test.expected)
			if test.code == "" {
				if err != nil {
					t.Fatalf("RequireSynchronized() error = %v", err)
				}
				return
			}
			if !IsCode(err, test.code) {
				t.Fatalf("error = %v; want code %q", err, test.code)
			}
			if strings.Contains(err.Error(), "private-path") || strings.Contains(err.Error(), revision) {
				t.Fatalf("error leaked input: %v", err)
			}
		})
	}
}

func TestRefreshReplacesWholeSnapshot(t *testing.T) {
	fake := &refresherFake{response: responseWith(api.TemplateSynchronized)}
	manager, _ := New(fake)
	manager.Initialize(context.Background())
	fake.response = responseWith(api.TemplateDifferent)
	manager.Refresh(context.Background())
	current, _ := manager.Current()
	if current.Templates[0].State != api.TemplateDifferent || fake.calls != 2 {
		t.Fatalf("current=%#v calls=%d", current, fake.calls)
	}
}

func responseWith(state api.TemplateState) api.TemplateStatusResponse {
	hashA := strings.Repeat("a", 64)
	hashB := hashA
	summary := api.TemplateStatusSummary{TemplateAvailable: true, SystemAvailable: true}
	if state == api.TemplateDifferent {
		hashB = strings.Repeat("c", 64)
	}
	if state == api.TemplateMissing {
		hashA = ""
		summary.TemplateAvailable = false
	}
	if state == api.TemplateInvalid {
		hashA, hashB = "", ""
		summary = api.TemplateStatusSummary{InvalidSide: api.TemplateSideBoth, Code: "both_missing"}
	}
	return api.TemplateStatusResponse{Templates: []api.TemplateStatus{{
		TemplateID: string(templatecatalog.XrayConfig), State: state,
		TemplateHash: hashA, SystemHash: hashB, Revision: strings.Repeat("b", 64), Summary: summary,
	}}}
}
