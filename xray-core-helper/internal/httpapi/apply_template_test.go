package httpapi

import (
	"context"
	"home-router/xray-core-helper/internal/safeapply"
	"home-router/xray-core-helper/internal/templateapply"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type applyTemplateFake struct {
	called       bool
	forceCalled  bool
	cancelCalled bool
	id           templatecatalog.ID
	generation   uint64
	err          error
	transaction  safeapply.Transaction
	pendingErr   error
	notPending   bool
}

func (fake *applyTemplateFake) Begin(_ context.Context, id templatecatalog.ID, generation uint64) (safeapply.Transaction, error) {
	fake.called = true
	fake.id = id
	fake.generation = generation
	return fake.transaction, fake.err
}
func (fake *applyTemplateFake) ApplyForce(ctx context.Context, id templatecatalog.ID, generation uint64) (safeapply.Transaction, error) {
	fake.forceCalled = true
	return fake.Begin(ctx, id, generation)
}
func (fake *applyTemplateFake) CancelReboot(context.Context, string) (safeapply.Transaction, bool, error) {
	fake.cancelCalled = true
	return fake.transaction, true, fake.err
}
func (fake *applyTemplateFake) Pending(context.Context) (*safeapply.Transaction, error) {
	if fake.transaction.ID == "" || fake.notPending {
		return nil, fake.pendingErr
	}
	return &fake.transaction, fake.pendingErr
}
func (fake *applyTemplateFake) Confirm(context.Context, string) (safeapply.Transaction, bool, error) {
	return fake.transaction, true, fake.err
}
func (fake *applyTemplateFake) Rollback(context.Context, string) (safeapply.Transaction, bool, error) {
	return fake.transaction, true, fake.err
}
func transactionFixture() safeapply.Transaction {
	return safeapply.Transaction{ID: "tx-1", TemplateID: templatecatalog.Nftables, BackupID: "backup", Deadline: time.Unix(200, 0).UTC(), Phase: safeapply.PhaseApplied, State: safeapply.StatePending, Generation: 8}
}
func TestApplyTemplateReturnsPendingTransactionWithoutSecrets(t *testing.T) {
	fake := &applyTemplateFake{transaction: transactionFixture(), notPending: true}
	handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{ApplyTemplate: fake}).Handler
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/templates/nftables/apply", strings.NewReader(`{"expectedGeneration":7}`)))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"transactionId":"tx-1"`) {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	if !fake.called || fake.id != templatecatalog.Nftables || fake.generation != 7 {
		t.Fatalf("fake=%#v", fake)
	}
	for _, secret := range []string{"privateKey", "/etc/nftables.conf", "secret command output"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("leak %q", secret)
		}
	}
}
func TestSafeApplyStatusConfirmAndRollback(t *testing.T) {
	for _, test := range []struct{ method, path, want string }{{"GET", "/v1/safe-apply", `"transactionId":"tx-1"`}, {"POST", "/v1/safe-apply/tx-1/confirm", `"changed":true`}, {"POST", "/v1/safe-apply/tx-1/rollback", `"changed":true`}} {
		fake := &applyTemplateFake{transaction: transactionFixture()}
		handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{ApplyTemplate: fake}).Handler
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != 200 || !strings.Contains(response.Body.String(), test.want) {
			t.Errorf("%s response=%d %s", test.path, response.Code, response.Body.String())
		}
	}
}

func TestSafeApplyStatusExposesRebootRequirement(t *testing.T) {
	value := transactionFixture()
	value.Phase = safeapply.PhaseAwaitingReboot
	fake := &applyTemplateFake{transaction: value}
	handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{ApplyTemplate: fake}).Handler
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/safe-apply", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"rebootRequired":true`) {
		t.Fatalf("missing reboot warning: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	writeApplyTemplateError(response, safeapply.ErrRebootRequired)
	if response.Code != 409 || !strings.Contains(response.Body.String(), `"code":"reboot_required"`) {
		t.Fatal("missing explicit error")
	}
}
func TestApplyTemplateStrictRequestAndStableErrors(t *testing.T) {
	tests := []struct {
		body    string
		service ApplyTemplateService
		status  int
		code    string
	}{{`{}`, &applyTemplateFake{}, 400, "invalid_request"}, {`{"expectedGeneration":1,"extra":true}`, &applyTemplateFake{}, 400, "invalid_request"}, {`{"expectedGeneration":1}`, &applyTemplateFake{err: &templateapply.Error{Code: templateapply.CodeUnknownTemplate}}, 400, "unknown_template"}, {`{"expectedGeneration":1}`, &applyTemplateFake{err: &templateapply.Error{Code: templateapply.CodeGeneration}}, 409, "template_generation_conflict"}, {`{"expectedGeneration":1}`, &applyTemplateFake{err: safeapply.ErrBusy}, 409, "safe_apply_pending"}}
	for _, test := range tests {
		handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{ApplyTemplate: test.service}).Handler
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/templates/other/apply", strings.NewReader(test.body)))
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
			t.Errorf("body=%s response=%d %s", test.body, response.Code, response.Body.String())
		}
	}
}

func TestForceTemplateAndCancelRebootAPI(t *testing.T) {
	for _, force := range []bool{false, true} {
		fake := &applyTemplateFake{transaction: transactionFixture(), notPending: true}
		fake.transaction.State = safeapply.StateConfirmed
		handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{ApplyTemplate: fake}).Handler
		response := httptest.NewRecorder()
		body := `{"expectedGeneration":7}`
		if force {
			body = `{"expectedGeneration":7,"force":true}`
		}
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/templates/nftables/apply", strings.NewReader(body)))
		if response.Code != 200 || fake.forceCalled != force {
			t.Fatal(response.Code, response.Body.String(), fake.forceCalled)
		}
	}
	fake := &applyTemplateFake{transaction: transactionFixture()}
	fake.transaction.Phase = safeapply.PhaseAwaitingReboot
	handler := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, ServerServices{ApplyTemplate: fake}).Handler
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/safe-apply/tx-1/cancel-reboot", strings.NewReader(`{}`)))
	if response.Code != 200 || !fake.cancelCalled {
		t.Fatal(response.Code, response.Body.String())
	}
	fake.transaction.State = safeapply.StateRebootCancelled
	if value := safeTransaction(fake.transaction); value.RebootRequired || value.RecoveryRequired {
		t.Fatal("resolved history reported as pending", value)
	}
}
