package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/logreader"
	"home-router/xray-core-helper/internal/routerdiagnostics"
	"home-router/xray-core-helper/internal/servicecontrol"
	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/xrayconfig"
	"home-router/xray-core-helper/internal/xrayvalidation"
)

type validationFake struct {
	result xrayvalidation.Result
	err    error
	calls  int
}

func (service *validationFake) Validate(context.Context) (xrayvalidation.Result, error) {
	service.calls++
	return service.result, service.err
}

type diagnosticsFake struct {
	result routerdiagnostics.Result
	err    error
}

func (service *diagnosticsFake) Diagnose(context.Context) (routerdiagnostics.Result, error) {
	return service.result, service.err
}

type serviceCall struct {
	unit   servicecontrol.Unit
	action servicecontrol.Action
}

type serviceFake struct {
	result servicecontrol.Result
	err    error
	calls  []serviceCall
}

func (service *serviceFake) Execute(_ context.Context, unit servicecontrol.Unit, action servicecontrol.Action) (servicecontrol.Result, error) {
	service.calls = append(service.calls, serviceCall{unit: unit, action: action})
	return service.result, service.err
}

type logCall struct {
	kind  logreader.Kind
	limit int
}

type logsFake struct {
	result logreader.Result
	err    error
	calls  []logCall
}

type loggingMutationFake struct {
	metadata transaction.Metadata
	err      error
	request  api.LogConfigurationMutationRequest
	calls    int
}

func (service *loggingMutationFake) Update(_ context.Context, request api.LogConfigurationMutationRequest) (transaction.Metadata, error) {
	service.calls++
	service.request = request
	return service.metadata, service.err
}

func (service *logsFake) Read(_ context.Context, kind logreader.Kind, limit int) (logreader.Result, error) {
	service.calls = append(service.calls, logCall{kind: kind, limit: limit})
	return service.result, service.err
}

func operationsServer(services ServerServices) *http.Server {
	return NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, services)
}

func TestValidationHTTPContract(t *testing.T) {
	validation := &validationFake{result: xrayvalidation.Result{Status: xrayvalidation.StatusValid}}
	server := operationsServer(ServerServices{Validation: validation})

	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/xray/config/validate", strings.NewReader(`{}`)))
	if response.Code != http.StatusOK || response.Body.String() != "{\"status\":\"valid\"}\n" || validation.calls != 1 {
		t.Fatalf("response=%d %q calls=%d", response.Code, response.Body.String(), validation.calls)
	}

	for _, test := range []struct {
		name, body, code string
		method           string
		status           int
	}{
		{name: "wrong method", method: http.MethodGet, code: "method_not_allowed", status: http.StatusMethodNotAllowed},
		{name: "nonempty body", method: http.MethodPost, body: `{"path":"/tmp/config"}`, code: "invalid_request", status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, httptest.NewRequest(test.method, "/v1/xray/config/validate", strings.NewReader(test.body)))
			assertAPIError(t, response, test.status, test.code)
		})
	}
}

func TestValidationHTTPMapsTypedFailuresWithoutDetails(t *testing.T) {
	validation := &validationFake{err: &xrayvalidation.Error{Code: xrayvalidation.CodeValidationFailed}}
	response := httptest.NewRecorder()
	operationsServer(ServerServices{Validation: validation}).Handler.ServeHTTP(
		response, httptest.NewRequest(http.MethodPost, "/v1/xray/config/validate", nil))
	assertAPIError(t, response, http.StatusUnprocessableEntity, "validation_failed")
}

func TestServiceHTTPMapsOnlyAllowlistedUnitsAndActions(t *testing.T) {
	tests := []struct {
		path   string
		unit   servicecontrol.Unit
		action servicecontrol.Action
	}{
		{path: "/v1/xray/services/xray/start", unit: servicecontrol.UnitXray, action: servicecontrol.ActionStart},
		{path: "/v1/xray/services/xray/stop", unit: servicecontrol.UnitXray, action: servicecontrol.ActionStop},
		{path: "/v1/xray/services/xray/restart", unit: servicecontrol.UnitXray, action: servicecontrol.ActionRestart},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			services := &serviceFake{result: servicecontrol.Result{Unit: test.unit, Action: test.action}}
			response := httptest.NewRecorder()
			operationsServer(ServerServices{Services: services}).Handler.ServeHTTP(
				response, httptest.NewRequest(http.MethodPost, test.path, nil))
			if response.Code != http.StatusOK || !reflect.DeepEqual(services.calls, []serviceCall{{unit: test.unit, action: test.action}}) {
				t.Fatalf("response=%d %s calls=%#v", response.Code, response.Body.String(), services.calls)
			}
		})
	}

	services := &serviceFake{}
	for _, path := range []string{
		"/v1/xray/services/ssh/restart",
		"/v1/xray/services/xray/status",
		"/v1/xray/services/xray.service/restart",
		"/v1/xray/services/xray/restart%2Fetc%2Fpasswd",
	} {
		response := httptest.NewRecorder()
		operationsServer(ServerServices{Services: services}).Handler.ServeHTTP(
			response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Errorf("path %q status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if len(services.calls) != 0 {
		t.Fatalf("invalid paths reached service controller: %#v", services.calls)
	}
}

func TestDiagnosticsHTTPReturnsOnlyTypedChecks(t *testing.T) {
	result := routerdiagnostics.Result{Checks: []routerdiagnostics.Check{
		{Name: routerdiagnostics.CheckDefaultRoute, Status: routerdiagnostics.StatusOK},
		{Name: routerdiagnostics.CheckDeclaredBindings, Status: routerdiagnostics.StatusMismatch, Code: "binding_mismatch", TemplateID: "nftables", BindingID: "port"},
	}}
	response := httptest.NewRecorder()
	operationsServer(ServerServices{Diagnostics: &diagnosticsFake{result: result}}).Handler.ServeHTTP(
		response, httptest.NewRequest(http.MethodGet, "/v1/xray/diagnostics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload api.DiagnosticsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Checks) != len(result.Checks) || payload.Checks[0] != (api.DiagnosticCheck{Name: "default_route", Status: "ok"}) {
		t.Fatalf("payload=%#v", payload)
	}
	if payload.Checks[1].Code != "binding_mismatch" || payload.Checks[1].BindingID != "port" {
		t.Fatal("relationship code lost in API projection")
	}
}

func TestLogHTTPContractRejectsArbitraryKindsPathsAndLimits(t *testing.T) {
	logs := &logsFake{result: logreader.Result{
		Kind: logreader.Access, Source: logreader.SourceFile, Lines: []string{"one"},
		BytesRead: 3, ByteLimit: logreader.MaxBytes, LineLimit: 25,
	}}
	server := operationsServer(ServerServices{Logs: logs})
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/logs/access?limit=25", nil))
	if response.Code != http.StatusOK || !reflect.DeepEqual(logs.calls, []logCall{{kind: logreader.Access, limit: 25}}) {
		t.Fatalf("response=%d %s calls=%#v", response.Code, response.Body.String(), logs.calls)
	}

	for _, path := range []string{
		"/v1/logs/audit", "/v1/logs/..%2Fetc%2Fpasswd", "/v1/logs/error?limit=0",
		"/v1/logs/error?limit=201", "/v1/logs/error?limit=10&path=/etc/passwd",
		"/v1/logs/error?limit=10&limit=11",
	} {
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Errorf("path %q status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if len(logs.calls) != 1 {
		t.Fatalf("invalid requests reached log reader: %#v", logs.calls)
	}
}

func TestOperationalHTTPUnavailableDependenciesFailClosed(t *testing.T) {
	server := operationsServer(ServerServices{})
	for _, test := range []struct {
		method, path, code string
	}{
		{http.MethodPost, "/v1/xray/config/validate", "validation_unavailable"},
		{http.MethodGet, "/v1/xray/diagnostics", "diagnostics_unavailable"},
		{http.MethodPost, "/v1/xray/services/xray/restart", "service_control_unavailable"},
		{http.MethodGet, "/v1/logs/error", "log_unavailable"},
	} {
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		assertAPIError(t, response, http.StatusServiceUnavailable, test.code)
	}
	loggingResponse := httptest.NewRecorder()
	loggingBody := `{"expectedGeneration":0,"access":{"mode":"file"},"error":{"mode":"file"},"loglevel":"info","dnsLog":true}`
	server.Handler.ServeHTTP(loggingResponse, httptest.NewRequest(http.MethodPut, "/v1/xray/logging", strings.NewReader(loggingBody)))
	assertAPIError(t, loggingResponse, http.StatusServiceUnavailable, "logging_unavailable")
}

func TestLoggingMutationHTTPContract(t *testing.T) {
	service := &loggingMutationFake{metadata: transaction.Metadata{Generation: 4, ContentHash: "safe-hash"}}
	server := operationsServer(ServerServices{Logging: service})
	body := `{"expectedGeneration":3,"restart":true,"access":{"mode":"file"},"error":{"mode":"journal"},"loglevel":"warning","dnsLog":false,"rotation":{"enabled":true}}`
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v1/xray/logging", strings.NewReader(body)))
	if response.Code != http.StatusOK || service.calls != 1 {
		t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), service.calls)
	}
	if service.request.ExpectedGeneration == nil || *service.request.ExpectedGeneration != 3 ||
		service.request.Access.Mode != api.LogTargetFile || service.request.Error.Mode != api.LogTargetJournal ||
		service.request.Rotation == nil || !service.request.Rotation.Enabled {
		t.Fatalf("request=%#v", service.request)
	}
	var payload api.MutationResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Generation != 4 || payload.ContentHash != "safe-hash" {
		t.Fatalf("payload=%#v", payload)
	}

	for _, invalid := range []string{
		`{"expectedGeneration":3,"access":{"mode":"file"},"error":{"mode":"file"},"loglevel":"info","dnsLog":true,"path":"/etc/passwd"}`,
		`{"expectedGeneration":3,"access":{"mode":"file","path":"/tmp/access"},"error":{"mode":"file"},"loglevel":"info","dnsLog":true}`,
		`{"expectedGeneration":3,"access":{"mode":"file"},"error":{"mode":"file"},"loglevel":"info","dnsLog":true,"rotation":{"enabled":true,"unit":"other.timer"}}`,
	} {
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v1/xray/logging", strings.NewReader(invalid)))
		assertAPIError(t, response, http.StatusBadRequest, "invalid_request")
	}
	if service.calls != 1 {
		t.Fatalf("invalid requests reached service: %d calls", service.calls)
	}
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d body=%s; want %d", response.Code, response.Body.String(), status)
	}
	var payload api.ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != code || strings.Contains(response.Body.String(), "SECRET") {
		t.Fatalf("error=%#v", payload.Error)
	}
}

func TestTProxyActivationRequiresDurableSafeApply(t *testing.T) {
	services := &serviceFake{}
	response := httptest.NewRecorder()
	operationsServer(ServerServices{Services: services}).Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/xray/services/tproxy/enable", nil))
	assertAPIError(t, response, 400, "invalid_service_action")
	for _, action := range []string{"start", "restart"} {
		response := httptest.NewRecorder()
		operationsServer(ServerServices{Services: services}).Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/xray/services/tproxy/"+action, nil))
		assertAPIError(t, response, 409, "safe_apply_required")
	}
	if len(services.calls) != 0 {
		t.Fatal("unjournaled network activation")
	}
}
