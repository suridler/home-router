package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type domainReconcileFake struct {
	calls   int
	applied bool
	err     error
}

func (f *domainReconcileFake) Reconcile(context.Context) (bool, error) {
	f.calls++
	return f.applied, f.err
}

func TestDomainReconcileEndpoint(t *testing.T) {
	for _, tt := range []struct {
		name, method, body string
		applied            bool
		err                error
		status, calls      int
	}{
		{name: "apply", method: http.MethodPost, body: `{}`, applied: true, status: 200, calls: 1},
		{name: "skip", method: http.MethodPost, body: `{}`, status: 200, calls: 1},
		{name: "wrong method", method: http.MethodGet, status: 405},
		{name: "untyped body", method: http.MethodPost, body: `{"path":"/tmp/arbitrary"}`, status: 400},
		{name: "failure", method: http.MethodPost, body: `{}`, err: errors.New("private information"), status: 503, calls: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := &domainReconcileFake{applied: tt.applied, err: tt.err}
			mux := http.NewServeMux()
			registerDomainReconcile(mux, fake)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(tt.method, "/v1/proxy-domains/reconcile", strings.NewReader(tt.body)))
			if response.Code != tt.status || fake.calls != tt.calls {
				t.Fatal(response.Code, fake.calls, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private information") {
				t.Fatal("error leaked")
			}
			if response.Code == 200 && !strings.Contains(response.Body.String(), map[bool]string{true: `"applied":true`, false: `"applied":false`}[tt.applied]) {
				t.Fatal(response.Body.String())
			}
		})
	}
}
