package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/acceptsystem"
	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type acceptSystemFake struct {
	result     acceptsystem.Result
	err        error
	generation uint64
	revision   string
}

func (fake *acceptSystemFake) Accept(_ context.Context, _ templatecatalog.ID, generation uint64, revision string) (acceptsystem.Result, error) {
	fake.generation = generation
	fake.revision = revision
	return fake.result, fake.err
}

func TestAcceptSystemAPIRequiresConcurrencyTokensAndReturnsNoOp(t *testing.T) {
	fake := &acceptSystemFake{result: acceptsystem.Result{TemplateID: "nftables", Accepted: false, Generation: 3, Revision: "revision", State: api.TemplateSynchronized}}
	handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{AcceptSystem: fake}).Handler
	for _, body := range []string{`{}`, `{"expectedGeneration":3}`, `{"expectedGeneration":3,"expectedRevision":"r","unknown":true}`, `{"expectedGeneration":3,"expectedGeneration":4,"expectedRevision":"r"}`} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/templates/nftables/accept-system", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Errorf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/templates/nftables/accept-system", strings.NewReader(`{"expectedGeneration":3,"expectedRevision":"r"}`)))
	if response.Code != 200 || fake.generation != 3 || fake.revision != "r" || !strings.Contains(response.Body.String(), `"accepted":false`) {
		t.Fatalf("status=%d body=%s fake=%#v", response.Code, response.Body.String(), fake)
	}
}

func TestAcceptSystemAPIErrorsAreStable(t *testing.T) {
	for _, test := range []struct {
		code   acceptsystem.Code
		status int
	}{{acceptsystem.CodeRevisionConflict, 409}, {acceptsystem.CodeGenerationConflict, 409}, {acceptsystem.CodeSourceMissing, 409}, {acceptsystem.CodeTargetMissing, 409}, {acceptsystem.CodeInvalidSystem, 422}, {acceptsystem.CodeStateInvalid, 422}, {acceptsystem.CodeBackupFailed, 503}} {
		fake := &acceptSystemFake{err: &acceptsystem.Error{Code: test.code}}
		handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{AcceptSystem: fake}).Handler
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/templates/nftables/accept-system", strings.NewReader(`{"expectedGeneration":1,"expectedRevision":"r"}`)))
		if response.Code != test.status || !strings.Contains(response.Body.String(), string(test.code)) {
			t.Errorf("code=%s response=%d %s", test.code, response.Code, response.Body.String())
		}
	}
}
