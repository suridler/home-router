package templatestatus

import (
	"bytes"
	"context"
	"home-router/xray-core-helper/internal/testconfig"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/nativerender"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatecompare"
	"home-router/xray-core-helper/internal/templatevalidation"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type concreteValidator struct {
	t     *testing.T
	paths []string
}

func TestXrayStatusRendersSettingsAndInvalidatesRevision(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	entry := templatecatalog.Entry{ID: templatecatalog.XrayConfig, WorkingPath: filepath.Join(root, "source.json"), SystemPath: filepath.Join(root, "system.json")}
	raw := []byte("{\n// helper:v1 bind level type=log-level source=helper.logging.level\n\"loglevel\": ${level}\n}\n")
	config := testconfig.Defaults()
	values := xrayconfig.TemplateValues{Logging: config.Logging}
	renderer := nativerender.Renderer{Xray: &values}
	render := func(ctx context.Context, _ templatecatalog.Entry, raw []byte) ([]byte, error) {
		return renderer.RenderXray(ctx, raw)
	}
	concrete, err := render(ctx, entry, raw)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{entry.WorkingPath: raw, entry.SystemPath: concrete} {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	comparer, _ := templatecompare.New(nil, config.Geodata.UserGeosite)
	validator := &concreteValidator{t: t}
	service := &Service{entries: []templatecatalog.Entry{entry}, comparer: comparer, validator: validator, userGeosite: config.Geodata.UserGeosite}
	service.SetNativeRenderer(render)
	first := service.Refresh(ctx).Templates[0]
	if first.State != api.TemplateSynchronized {
		t.Fatalf("Xray source not rendered: %+v", first)
	}
	if len(validator.paths) != 2 || validator.paths[0] == entry.WorkingPath || validator.paths[1] != entry.SystemPath {
		t.Fatal("unrendered source validated")
	}
	if _, err := os.Stat(validator.paths[0]); !os.IsNotExist(err) {
		t.Fatal("temporary validation candidate retained")
	}
	values.Logging.Level = "error"
	next := service.Refresh(ctx).Templates[0]
	if next.State != api.TemplateDifferent || first.Revision == next.Revision {
		t.Fatal("settings change failed to invalidate comparison/revision")
	}
	values.Logging.Level = "invalid"
	failed := service.Refresh(ctx).Templates[0]
	if failed.State != api.TemplateInvalid || failed.Summary.InvalidSide != api.TemplateSideTemplate {
		t.Fatal("invalid settings treated as synchronized")
	}
	got, err := os.ReadFile(entry.WorkingPath)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("status changed source", err)
	}
}

func (v *concreteValidator) Validate(_ context.Context, _ templatecatalog.ID, path string) (templatevalidation.Result, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		v.t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("${")) {
		v.t.Fatal("unresolved factory template sent to validator")
	}
	v.paths = append(v.paths, path)
	return templatevalidation.Result{}, nil
}

func TestNativeStatusComparesPreparedCandidateWithoutRewritingTemplate(t *testing.T) {
	for _, id := range []templatecatalog.ID{templatecatalog.Nftables, templatecatalog.Sysctl} {
		t.Run(string(id), func(t *testing.T) {
			root := t.TempDir()
			entry := templatecatalog.Entry{ID: id, WorkingPath: filepath.Join(root, "working.conf"), SystemPath: filepath.Join(root, "system.conf")}
			raw := []byte("# helper:v1 bind selected type=uint32 source=helper.network.intercept-mark\ncustom = ${intercept_mark}\n")
			if id == templatecatalog.Nftables {
				raw = append([]byte("# helper:begin gateway\n"), raw...)
				raw = append(raw, []byte("# helper:end gateway\n")...)
			}
			renderer := nativerender.Renderer{Network: testconfig.Defaults().Network}
			render := func(ctx context.Context, _ templatecatalog.Entry, b []byte) ([]byte, error) {
				return renderer.Render(ctx, b)
			}
			concrete, err := render(context.Background(), entry, raw)
			if err != nil {
				t.Fatal(err)
			}
			os.WriteFile(entry.WorkingPath, raw, 0600)
			os.WriteFile(entry.SystemPath, concrete, 0600)
			comparer, _ := templatecompare.New(nil)
			validator := &concreteValidator{t: t}
			service := &Service{entries: []templatecatalog.Entry{entry}, comparer: comparer, validator: validator}
			service.SetNativeRenderer(render)
			first := service.Refresh(context.Background()).Templates[0]
			if first.State != api.TemplateSynchronized {
				t.Fatalf("unresolved source compared literally: %+v", first)
			}
			if len(validator.paths) != 2 || validator.paths[0] == entry.WorkingPath || validator.paths[1] != entry.SystemPath {
				t.Fatal("wrong validation targets")
			}
			if _, err := os.Stat(validator.paths[0]); !os.IsNotExist(err) {
				t.Fatal("candidate not cleaned")
			}
			renderer.Network.InterceptMark = 2
			next := service.Refresh(context.Background()).Templates[0]
			if next.State != api.TemplateDifferent || first.Revision == next.Revision {
				t.Fatal("binding change did not invalidate comparison/revision")
			}
			actual, _ := os.ReadFile(entry.WorkingPath)
			if !bytes.Equal(actual, raw) {
				t.Fatal("status rewrote template")
			}
		})
	}
}
