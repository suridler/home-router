package httpapi

import (
	"context"
	"home-router/xray-core-helper/internal/api"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type errorsFake struct {
	limit     int
	collected int
}

func (f *errorsFake) List(_ context.Context, n int) (api.ErrorHistoryResponse, error) {
	f.limit = n
	return api.ErrorHistoryResponse{Errors: []api.ErrorEvent{{ID: 2, Message: "redacted"}}, Capacity: 100}, nil
}
func (f *errorsFake) Collect(context.Context) error { f.collected++; return nil }
func TestErrorHistoryContractsAreFixedAndBounded(t *testing.T) {
	fake := &errorsFake{}
	mux := http.NewServeMux()
	registerErrorHistory(mux, fake)
	for _, path := range []string{"/v1/errors?limit=0", "/v1/errors?limit=101", "/v1/errors?limit=2&limit=3", "/v1/errors?path=/etc/passwd"} {
		r := httptest.NewRecorder()
		mux.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code != 400 {
			t.Fatal(path, r.Code)
		}
	}
	r := httptest.NewRecorder()
	mux.ServeHTTP(r, httptest.NewRequest("GET", "/v1/errors?limit=5", nil))
	if r.Code != 200 || fake.limit != 5 || !strings.Contains(r.Body.String(), "redacted") {
		t.Fatal(r.Code, r.Body)
	}
	r = httptest.NewRecorder()
	mux.ServeHTTP(r, httptest.NewRequest("POST", "/v1/errors/collect", strings.NewReader(`{"path":"/tmp/other"}`)))
	if r.Code != 400 || fake.collected != 0 {
		t.Fatal("arbitrary collection accepted")
	}
	r = httptest.NewRecorder()
	mux.ServeHTTP(r, httptest.NewRequest("POST", "/v1/errors/collect", strings.NewReader(`{}`)))
	if r.Code != 200 || fake.collected != 1 || fake.limit != 5 {
		t.Fatal(r.Code, r.Body)
	}
}
