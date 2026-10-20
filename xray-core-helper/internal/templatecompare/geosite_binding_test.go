package templatecompare

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
)

func TestCompareBoundXrayWithoutChangingTemplate(t *testing.T) {
	raw := []byte(`{
 // helper:v1 bind user-geosite type=geosite-reference source=helper.user-geosite token="\"ext:geosite_original.dat:USERLIST\""
 "domain":["ext:geosite_original.dat:USERLIST"]
}`)
	root := t.TempDir()
	entry := templatecatalog.Entry{ID: templatecatalog.XrayConfig, WorkingPath: filepath.Join(root, "working"), SystemPath: filepath.Join(root, "system")}
	rendered, err := xrayconfig.BindUserGeosite(raw, "geosite_user.dat")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.WorkingPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.SystemPath, rendered, 0600); err != nil {
		t.Fatal(err)
	}
	comparer, _ := New(nil, "geosite_user.dat")
	status := comparer.Compare(context.Background(), entry)
	if status.State != api.TemplateSynchronized {
		t.Fatalf("state: %+v", status)
	}
	changed, _ := New(nil, "geosite_other.dat")
	next := changed.Compare(context.Background(), entry)
	if next.State != api.TemplateDifferent || next.Revision == status.Revision {
		t.Fatal("config change hidden")
	}
	actual, err := os.ReadFile(entry.WorkingPath)
	if err != nil || string(actual) != string(raw) {
		t.Fatal("working template modified", err)
	}
	if err := os.Remove(entry.WorkingPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(entry.SystemPath, entry.WorkingPath); err != nil {
		t.Fatal(err)
	}
	if comparer.Compare(context.Background(), entry).State != api.TemplateInvalid {
		t.Fatal("symlink accepted")
	}
}
