package logpathcheck

import (
	"context"
	"errors"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/bootstrap"
)

func propertyFixture(overrides map[string]string) string {
	p := map[string]string{"ProtectSystem": "no", "ProtectHome": "no", "PrivateTmp": "no"}
	for k, v := range overrides {
		p[k] = v
	}
	var lines []string
	for _, k := range properties {
		lines = append(lines, k+"="+p[k])
	}
	return strings.Join(lines, "\n")
}

func TestRestrictionsAreWarningsForConfiguredDestinations(t *testing.T) {
	for _, tt := range []struct {
		name, path string
		properties map[string]string
		want       bool
	}{
		{"standard", "/var/log/xray/error.log", map[string]string{"ProtectSystem": "strict", "ReadWritePaths": "/var/log/xray"}, false},
		{"custom", "/opt/custom/error.log", map[string]string{"ProtectSystem": "strict", "ReadWritePaths": "/var/log/xray"}, true},
		{"prefix-is-not-directory", "/var/log/xray-other/error.log", map[string]string{"ProtectSystem": "strict", "ReadWritePaths": "/var/log/xray"}, true},
		{"unrestricted", "/opt/custom/error.log", nil, false},
		{"read-only", "/var/log/xray/error.log", map[string]string{"ReadOnlyPaths": "/var/log"}, true},
		{"nested-read-only", "/var/log/xray/private/error.log", map[string]string{"ReadWritePaths": "/var/log/xray", "ReadOnlyPaths": "/var/log/xray/private"}, true},
		{"home", "/home/test/log/error.log", map[string]string{"ProtectHome": "yes"}, true},
		{"inaccessible", "/opt/custom/error.log", map[string]string{"InaccessiblePaths": "/opt/custom", "ReadWritePaths": "/opt/custom"}, true},
		{"unknown-list", "/opt/custom/error.log", map[string]string{"ReadWritePaths": "\"/opt/space path\""}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := parse([]byte(propertyFixture(tt.properties)))
			if !ok || restricted(tt.path, p) != tt.want {
				t.Fatal("unexpected restriction assessment")
			}
		})
	}
}

func TestCheckerSeparatesServicesAndDoesNotInspectJournal(t *testing.T) {
	calls := 0
	c := &Checker{show: func(_ context.Context, unit string) ([]byte, error) {
		calls++
		if unit == "xray.service" {
			return []byte(propertyFixture(nil)), nil
		}
		return []byte(propertyFixture(map[string]string{"ProtectSystem": "strict", "ReadWritePaths": "/var/log/xray"})), nil
	}}
	r, err := c.Check(context.Background(), bootstrap.LoggingConfig{AccessPath: "none"})
	if err != nil || r.Restricted() || calls != 0 {
		t.Fatal("journal performed service inspection")
	}
	r, err = c.Check(context.Background(), bootstrap.LoggingConfig{AccessPath: "/custom/access.log"})
	if err != nil || !r.HelperAccess || r.XrayAccess || r.HelperError || calls != 2 {
		t.Fatal("service restrictions conflated")
	}
	c.show = func(context.Context, string) ([]byte, error) { return nil, errors.New("unavailable") }
	r, err = c.Check(context.Background(), bootstrap.LoggingConfig{ErrorPath: "/custom/error.log"})
	if err != nil || !r.HelperError || !r.XrayError {
		t.Fatal("unknown access treated as allowed")
	}
}
