package templateapply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/templatecatalog"
)

const userGeositeTemplate = `{"routing":{"rules":[{"domain":[
// helper:v1 bind user-geosite type=geosite-reference source=helper.user-geosite token="\"ext:geosite_template.dat:USERLIST\""
"ext:geosite_template.dat:USERLIST"]}]}}`

func TestXrayCandidateBindsOnlyStagedCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "working.jsonc")
	if err := os.WriteFile(path, []byte(userGeositeTemplate), 0600); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{userGeosite: "geosite_user.dat", hash: nativeHash}
	entry := templatecatalog.Entry{ID: templatecatalog.XrayConfig, WorkingPath: path}
	candidate, err := engine.candidate(context.Background(), entry, true)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(candidate.path)
	if err != nil || !strings.Contains(string(content), "ext:geosite_user.dat:USERLIST") {
		t.Fatalf("candidate %s %v", content, err)
	}
	working, _ := os.ReadFile(path)
	if string(working) != userGeositeTemplate || candidate.path == path {
		t.Fatal("working template changed")
	}
	firstHash := candidate.hash
	candidate.close()
	if _, err := os.Stat(candidate.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temporary candidate retained", err)
	}
	// A helper setting change between prepare and activation changes the hash,
	// which the existing confirmed transaction must reject.
	engine.userGeosite = "geosite_changed.dat"
	changed, err := engine.candidate(context.Background(), entry, false)
	if err != nil {
		t.Fatal(err)
	}
	defer changed.close()
	if changed.hash == firstHash {
		t.Fatal("setting change did not invalidate prepared candidate")
	}
}

func TestCrossCheckerRejectsMismatchedHelperGeositeBinding(t *testing.T) {
	checker := bindingChecker(t, userGeositeTemplate)
	checker.userGeosite = "geosite_user.dat"
	if err := checker.Check(context.Background(), templatecatalog.XrayConfig, "/candidate"); !IsCode(err, CodeBindingMismatch) {
		t.Fatalf("mismatch = %v", err)
	}
	checker.userGeosite = "geosite_template.dat"
	if err := checker.Check(context.Background(), templatecatalog.XrayConfig, "/candidate"); err != nil {
		t.Fatal(err)
	}
}
