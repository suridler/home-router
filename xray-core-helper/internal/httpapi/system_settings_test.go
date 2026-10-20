package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/systemsettings"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type settingsFake struct{ value int }

func (fake *settingsFake) Get(context.Context) api.SystemSettingsResponse {
	return api.SystemSettingsResponse{Backup: api.BackupSettings{RetainPerTemplate: fake.value}}
}
func (fake *settingsFake) Put(_ context.Context, request api.UpdateSystemSettingsRequest) (api.SystemSettingsResponse, error) {
	if request.Backup.RetainPerTemplate < 1 || request.Backup.RetainPerTemplate > 100 {
		return api.SystemSettingsResponse{}, systemsettings.ErrInvalidSettings
	}
	fake.value = request.Backup.RetainPerTemplate
	return fake.Get(context.Background()), nil
}

func TestSystemSettingsAPIIsStrict(t *testing.T) {
	fake := &settingsFake{value: 10}
	handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{Settings: fake}).Handler
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/settings", nil))
	if get.Code != 200 || !strings.Contains(get.Body.String(), `"retainPerTemplate":10`) {
		t.Fatalf("GET %d %s", get.Code, get.Body.String())
	}
	for _, body := range []string{`{"backup":{"retainPerTemplate":0}}`, `{"backup":{"retainPerTemplate":101}}`, `{"backup":{"retainPerTemplate":10},"unknown":true}`, `{"backup":{"retainPerTemplate":10,"retainPerTemplate":11}}`} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Errorf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(`{"backup":{"retainPerTemplate":20}}`)))
	if response.Code != 200 || fake.value != 20 {
		t.Fatalf("PUT status=%d value=%d", response.Code, fake.value)
	}
}
