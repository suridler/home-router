package templateapply

import (
	"context"
	"errors"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatevalidation"
)

type validatorStub struct{ err error }

func (stub validatorStub) Validate(context.Context, templatecatalog.ID, string) (templatevalidation.Result, error) {
	return templatevalidation.Result{}, stub.err
}

func bindingChecker(t *testing.T, content string) *CrossChecker {
	t.Helper()
	checker, err := NewCrossChecker(validatorStub{})
	if err != nil {
		t.Fatal(err)
	}
	xray, _ := checker.catalog.Lookup(templatecatalog.XrayConfig)
	checker.read = func(path string) ([]byte, error) {
		if path == "/candidate" {
			return []byte(content), nil
		}
		if path == xray.SystemPath {
			return []byte("{\"inbounds\":[{\"tag\":\"tproxy-in\",\"port\":60000}]}"), nil
		}
		return nil, errors.New("unavailable")
	}
	checker.identity = func(context.Context) (int, error) { t.Fatal("undeclared identity lookup"); return 0, nil }
	return checker
}

func TestUnmarkedNativeFilesHaveNoImplicitRules(t *testing.T) {
	for id, content := range map[templatecatalog.ID]string{
		templatecatalog.Nftables:   "table ip custom { chain arbitrary {} }\n",
		templatecatalog.TProxyUnit: "[Service]\nExecStart=/custom/uninterpreted --anything\n",
		templatecatalog.Sysctl:     "kernel.custom = arbitrary native value\n",
	} {
		checker := bindingChecker(t, content)
		read := checker.read
		checker.read = func(path string) ([]byte, error) {
			if path != "/candidate" {
				t.Fatal("unrequested dependency read")
			}
			return read(path)
		}
		if err := checker.Check(context.Background(), id, "/candidate"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeclaredBindingsReadServiceSelectedXrayConfig(t *testing.T) {
	catalog, err := templatecatalog.ForXrayConfig("/etc/xray/service-selected.json")
	if err != nil {
		t.Fatal(err)
	}
	checker, err := NewCrossCheckerWithCatalog(validatorStub{}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	readConfig := false
	checker.read = func(path string) ([]byte, error) {
		switch path {
		case "/candidate":
			return []byte("# helper:v1 bind endpoint type=port source=xray.tproxy-port token=\"60000\"\ndefine endpoint = 60000\n"), nil
		case "/etc/xray/service-selected.json":
			readConfig = true
			return []byte(`{"inbounds":[{"tag":"tproxy-in","port":60000}]}`), nil
		default:
			return nil, errors.New("unavailable")
		}
	}
	if err := checker.Check(context.Background(), templatecatalog.Nftables, "/candidate"); err != nil {
		t.Fatal(err)
	}
	if !readConfig {
		t.Fatal("service-selected config was not checked")
	}
}

func TestOnlyDeclaredXrayRelationshipsAreChecked(t *testing.T) {
	for _, tt := range []struct {
		value string
		code  Code
	}{{"60000", ""}, {"52345", CodeBindingMismatch}} {
		content := "# helper:v1 bind endpoint type=port source=xray.tproxy-port token=\"" + tt.value + "\"\ndefine anything = " + tt.value + "\n"
		checker := bindingChecker(t, content)
		err := checker.Check(context.Background(), templatecatalog.Nftables, "/candidate")
		if tt.code == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else if !IsCode(err, tt.code) {
			t.Fatal(err)
		}
	}
}

func TestMalformedAndUnresolvedBindingsFailSafely(t *testing.T) {
	for _, tt := range []struct {
		content string
		code    Code
	}{
		{"# helper:v2 invalid\n", CodeBindingInvalid},
		{"# helper:v1 bind mark type=uint32 source=xray.intercept-mark token=\"1\"\ndefine unrelated = 1\n", CodeBindingUnresolved},
		{"# helper:v1 bind port type=port source=native.tproxy-unit.missing token=\"1234\"\ndefine arbitrary = 1234\n", CodeBindingUnresolved},
	} {
		checker := bindingChecker(t, tt.content)
		if err := checker.Check(context.Background(), templatecatalog.Nftables, "/candidate"); !IsCode(err, tt.code) {
			t.Fatal(err)
		}
	}
}

func TestIdentityRunsOnlyForExplicitBinding(t *testing.T) {
	checker := bindingChecker(t, "# helper:v1 bind group type=uint32 source=xray.service-gid token=\"42\"\ndefine socket_group=42\n")
	checker.identity = func(context.Context) (int, error) { return 42, nil }
	if err := checker.Check(context.Background(), templatecatalog.Nftables, "/candidate"); err != nil {
		t.Fatal(err)
	}
	checker.identity = func(context.Context) (int, error) { return 0, errors.New("secret identity details") }
	err := checker.Check(context.Background(), templatecatalog.Nftables, "/candidate")
	if !IsCode(err, CodeBindingUnresolved) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}

func TestNativeValidatorRejectsBeforeRelationshipReads(t *testing.T) {
	checker, _ := NewCrossChecker(validatorStub{err: errors.New("private native output")})
	checker.read = func(string) ([]byte, error) { t.Fatal("read after rejected candidate"); return nil, nil }
	if err := checker.Check(context.Background(), templatecatalog.Sysctl, "/candidate"); !IsCode(err, CodeValidation) {
		t.Fatal(err)
	}
}
