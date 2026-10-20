package templatestatus

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatevalidation"
)

type bindingValidator struct {
	t     *testing.T
	paths []string
}

func (v *bindingValidator) Validate(_ context.Context, _ templatecatalog.ID, path string) (templatevalidation.Result, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		v.t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ext:geosite_user.dat:USERLIST") {
		v.t.Fatal("unbound candidate validated")
	}
	v.paths = append(v.paths, path)
	return templatevalidation.Result{}, nil
}
func TestStatusValidatesRenderedWorkingCandidateAndActualSystem(t *testing.T) {
	raw := []byte(`{
 // helper:v1 bind user-geosite type=geosite-reference source=helper.user-geosite token="\"ext:geosite_old.dat:USERLIST\""
 "domain":["ext:geosite_old.dat:USERLIST"]
}`)
	root := t.TempDir()
	entry := templatecatalog.Entry{ID: templatecatalog.XrayConfig, WorkingPath: filepath.Join(root, "working"), SystemPath: filepath.Join(root, "system")}
	if err := os.WriteFile(entry.WorkingPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.SystemPath, []byte(`{"domain":["ext:geosite_user.dat:USERLIST"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	validator := &bindingValidator{t: t}
	service := &Service{validator: validator, userGeosite: "geosite_user.dat"}
	status := synchronized(entry.ID)
	status.Summary.TemplateAvailable = true
	status.Summary.SystemAvailable = true
	if got := service.validateAvailable(context.Background(), entry, status); got.State == api.TemplateInvalid {
		t.Fatal(got)
	}
	if len(validator.paths) != 2 || validator.paths[0] == entry.WorkingPath || validator.paths[1] != entry.SystemPath {
		t.Fatal("incorrect validation paths")
	}
	if _, err := os.Stat(validator.paths[0]); !os.IsNotExist(err) {
		t.Fatal("temporary candidate retained")
	}
	actual, err := os.ReadFile(entry.WorkingPath)
	if err != nil || string(actual) != string(raw) {
		t.Fatal("template modified")
	}
}
