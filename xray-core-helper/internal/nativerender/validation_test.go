package nativerender

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatevalidation"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type inspectValidator struct {
	t        *testing.T
	expected []byte
	path     string
}

func TestValidateStoredXrayPreservesConcreteRollbackEvidence(t *testing.T) {
	for _, concrete := range []bool{false, true} {
		t.Run(map[bool]string{false: "source", true: "concrete"}[concrete], func(t *testing.T) {
			raw := []byte("{\n// helper:v1 bind level type=log-level source=helper.logging.level\n\"loglevel\": ${level}\n}\n")
			values := xrayconfig.TemplateValues{Logging: bootstrap.Defaults().Logging}
			bound, err := xrayconfig.RenderTemplate(raw, values)
			if err != nil {
				t.Fatal(err)
			}
			input := raw
			if concrete {
				input = bound
				values.Logging.Level = "error"
			}
			path := filepath.Join(t.TempDir(), "source.json")
			if err := os.WriteFile(path, input, 0600); err != nil {
				t.Fatal(err)
			}
			renderer := Renderer{Catalog: templatecatalog.Default(), Xray: &values}
			validator := &inspectValidator{t: t, expected: bound}
			if err := renderer.ValidateStored(context.Background(), templatecatalog.XrayConfig, path, validator); err != nil {
				t.Fatal(err)
			}
			if concrete && validator.path != path || !concrete && validator.path == path {
				t.Fatal("wrong validation path")
			}
			if !concrete {
				if _, err := os.Stat(validator.path); !os.IsNotExist(err) {
					t.Fatal("temporary candidate retained")
				}
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, input) {
				t.Fatal("backup evidence changed", err)
			}
		})
	}
}

func (v *inspectValidator) Validate(_ context.Context, _ templatecatalog.ID, path string) (templatevalidation.Result, error) {
	v.path = path
	raw, err := os.ReadFile(path)
	if err != nil {
		v.t.Fatal(err)
	}
	if !bytes.Equal(raw, v.expected) {
		v.t.Fatal("wrong bytes validated")
	}
	return templatevalidation.Result{}, nil
}

func TestValidateStoredPreparesSourceButPreservesConcreteEvidence(t *testing.T) {
	for _, concrete := range []bool{false, true} {
		t.Run(map[bool]string{false: "source", true: "concrete"}[concrete], func(t *testing.T) {
			source := []byte("# helper:v1 bind flag type=uint32 source=helper.network.intercept-mark\ncustom = ${value}\n")
			rendered := []byte("# helper:v1 bind flag type=uint32 source=helper.network.intercept-mark token=\"1\"\ncustom = 1\n")
			input := source
			if concrete {
				input = rendered
			}
			path := filepath.Join(t.TempDir(), "backup")
			if err := os.WriteFile(path, input, 0600); err != nil {
				t.Fatal(err)
			}
			renderer := Renderer{Network: bootstrap.Defaults().Network, Catalog: templatecatalog.Default()}
			// Concrete rollback evidence must not be silently rebound to current settings.
			if concrete {
				renderer.Network.InterceptMark = 2
			}
			validator := &inspectValidator{t: t, expected: rendered}
			if err := renderer.ValidateStored(context.Background(), templatecatalog.Sysctl, path, validator); err != nil {
				t.Fatal(err)
			}
			if concrete && validator.path != path || !concrete && validator.path == path {
				t.Fatal("wrong validation path")
			}
			if !concrete {
				if _, err := os.Stat(validator.path); !os.IsNotExist(err) {
					t.Fatal("candidate retained")
				}
			}
			actual, _ := os.ReadFile(path)
			if !bytes.Equal(actual, input) {
				t.Fatal("backup changed")
			}
		})
	}
}
