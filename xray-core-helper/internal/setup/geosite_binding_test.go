package setup

import (
	"home-router/xray-core-helper/internal/templatecatalog"
	"strings"
	"testing"
)

func TestXraySetupRenderingBindsConfiguredGeosite(t *testing.T) {
	raw := []byte(`{"routing":{"rules":[{"domain":[
// helper:v1 bind user-geosite type=geosite-reference source=helper.user-geosite token="\"ext:geosite_template.dat:USERLIST\""
"ext:geosite_template.dat:USERLIST"]}]}}`)
	bound, err := renderNative(templatecatalog.XrayConfig, raw, Plan{}, "geosite_chosen.dat")
	if err != nil || !strings.Contains(string(bound), "ext:geosite_chosen.dat:USERLIST") {
		t.Fatalf("render %s %v", bound, err)
	}
	preserved, err := renderNative(templatecatalog.XrayConfig, raw, Plan{PreserveXray: true}, "geosite_chosen.dat")
	if err != nil || string(preserved) != string(raw) {
		t.Fatal("adopting existing config changed bytes", err)
	}
}
