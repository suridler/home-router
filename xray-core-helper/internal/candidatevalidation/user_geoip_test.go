package candidatevalidation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCustomGeositeValidationRetainsConfiguredUserGeoip(t *testing.T) {
	fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:USERLIST"]},{"ip":["ext:geoip_custom.dat:USERLIST"]}]}}`)
	fixture.config.Geodata.UserGeoip = "geoip_custom.dat"
	writeAsset(t, filepath.Join(fixture.companionDir, "geoip_custom.dat"), "user geoip")
	sourceDir := filepath.Join(fixture.companionDir, ".xray-core-helper-consolidation-pending")
	if err := os.Mkdir(sourceDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeAsset(t, filepath.Join(sourceDir, "geosite_user.dat"), "candidate geosite")
	runner := &fakeRunner{onRun: func(ctx context.Context, binary string, args, env []string) error {
		dir := strings.TrimPrefix(env[0], "XRAY_LOCATION_ASSET=")
		data, err := os.ReadFile(filepath.Join(dir, "geoip_custom.dat"))
		if err != nil || string(data) != "user geoip" {
			t.Fatal("configured user geoip missing", err)
		}
		return nil
	}}
	validator := fixture.validator(t, runner)
	hash := sha256.Sum256([]byte("candidate geosite"))
	source := filepath.Join(fixture.config.Xray.AssetDir, ".xray-core-helper-consolidation-pending", "geosite_user.dat")
	if err := validator.ValidateCustomGeosite(context.Background(), source, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	if !runner.called {
		t.Fatal("Xray validator not called")
	}
}

func TestStandardValidationRetainsConfiguredUserGeoip(t *testing.T) {
	fixture := newFixture(t, `{"routing":{"rules":[{"ip":["ext:geoip_custom.dat:USERLIST"]}]}}`)
	fixture.config.Geodata.UserGeoip = "geoip_custom.dat"
	writeAsset(t, fixture.download.GeositePath, "download geosite")
	writeAsset(t, fixture.download.GeoipPath, "download geoip")
	writeAsset(t, filepath.Join(fixture.companionDir, "geoip_custom.dat"), "user geoip")
	runner := &fakeRunner{onRun: func(ctx context.Context, binary string, args, env []string) error {
		dir := strings.TrimPrefix(env[0], "XRAY_LOCATION_ASSET=")
		data, err := os.ReadFile(filepath.Join(dir, "geoip_custom.dat"))
		if err != nil || string(data) != "user geoip" {
			t.Fatal("configured user geoip missing", err)
		}
		return nil
	}}
	validator := fixture.validator(t, runner)
	if err := validator.Validate(context.Background(), fixture.download); err != nil {
		t.Fatal(err)
	}
	if !runner.called {
		t.Fatal("Xray validator not called")
	}
}
