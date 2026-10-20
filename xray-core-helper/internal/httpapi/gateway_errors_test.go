package httpapi

import (
	"home-router/xray-core-helper/internal/templateapply"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeRelationshipErrorsRetainSpecificSafeApplyCode(t *testing.T) {
	for _, code := range []templateapply.Code{templateapply.CodeBindingInvalid, templateapply.CodeBindingMismatch, templateapply.CodeBindingUnresolved} {
		r := httptest.NewRecorder()
		writeApplyTemplateError(r, &templateapply.Error{Code: code})
		if r.Code != 422 || !strings.Contains(r.Body.String(), string(code)) {
			t.Fatalf("%s: %d %s", code, r.Code, r.Body.String())
		}
	}
}
