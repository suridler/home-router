package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type templateStatusFake struct {
	current api.TemplateStatusResponse
	ready   bool
	refresh api.TemplateStatusResponse
}

func (fake *templateStatusFake) Current() (api.TemplateStatusResponse, bool) {
	return fake.current, fake.ready
}
func (fake *templateStatusFake) Refresh(context.Context) api.TemplateStatusResponse {
	return fake.refresh
}

func TestTemplateStatusAndRefreshPreserveCatalogOrder(t *testing.T) {
	rows := []api.TemplateStatus{{TemplateID: "xray-config", State: api.TemplateDifferent, Revision: "one"}, {TemplateID: "nftables", State: api.TemplateSynchronized, Revision: "two"}}
	fake := &templateStatusFake{current: api.TemplateStatusResponse{Templates: rows}, ready: true, refresh: api.TemplateStatusResponse{Templates: rows}}
	handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{Templates: fake}).Handler
	for _, test := range []struct{ method, path string }{{http.MethodGet, "/v1/templates"}, {http.MethodPost, "/v1/templates/refresh"}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: status %d body %s", test.path, response.Code, response.Body.String())
		}
		body := response.Body.String()
		if strings.Index(body, "xray-config") > strings.Index(body, "nftables") {
			t.Fatalf("catalog order changed: %s", body)
		}
		if strings.Contains(body, "config.json") || strings.Contains(body, "privateKey") {
			t.Fatalf("unsafe detail leaked: %s", body)
		}
	}
}

func TestTemplateStatusUnavailableAndRefreshBody(t *testing.T) {
	handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil).Handler
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/templates", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
	fake := &templateStatusFake{ready: true}
	handler = NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{Templates: fake}).Handler
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/templates/refresh", strings.NewReader("{}")))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
}
