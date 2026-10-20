package routerdiagnostics

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/nativebinding"
	"testing"
)

func TestDiagnosticsReportsOnlyExplicitRelationships(t *testing.T) {
	r := &fakeRunner{replies: []commandReply{{output: []byte("default dev eth0")}, {output: []byte("active")}, {output: []byte("active")}}}
	service := testService(r)
	service.bindings = func(context.Context) ([]nativebinding.Check, error) {
		return []nativebinding.Check{
			{TemplateID: "nftables", BindingID: "my_port", Status: "mismatch"},
			{TemplateID: "sysctl", BindingID: "install_input", Status: "setup_only"},
		}, nil
	}
	result, err := service.Diagnose(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Checks[3].BindingID != "my_port" || result.Checks[3].Status != StatusMismatch || result.Checks[4].Status != StatusNotDeclared {
		t.Fatal("declared identities lost")
	}
	if len(r.calls) != 3 {
		t.Fatal("inferred native commands")
	}
}
func TestUnreadableDeclarationsDoNotBecomeSuccess(t *testing.T) {
	r := &fakeRunner{replies: []commandReply{{output: []byte("default dev eth0")}, {output: []byte("active")}, {output: []byte("active")}}}
	service := testService(r)
	service.bindings = func(context.Context) ([]nativebinding.Check, error) { return nil, errors.New("private configuration") }
	result, err := service.Diagnose(context.Background())
	if err != nil || result.Checks[3].Status != StatusUnavailable || result.Checks[3].Code != "declared_bindings_unavailable" {
		t.Fatal("unavailable declarations accepted")
	}
}
