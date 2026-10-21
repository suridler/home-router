package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebDefaultsAndTOMLOverrides(t *testing.T) {
	want := WebConfig{ExporterURL: "http://127.0.0.1:9101", Host: "127.0.0.1", Port: 3000, ControlEnabled: true}
	if got := Defaults().Web; got != want {
		t.Fatalf("defaults = %#v", got)
	}
	path := filepath.Join(t.TempDir(), "helper.toml")
	if err := os.WriteFile(path, []byte("[web]\nhost = '192.0.2.1'\nport = 3001\ncontrol_enabled = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := testLoad(path)
	if err != nil {
		t.Fatal(err)
	}
	want = WebConfig{ExporterURL: "http://127.0.0.1:9101", Host: "192.0.2.1", Port: 3001, ControlEnabled: false}
	if config.Web != want {
		t.Fatalf("loaded = %#v", config.Web)
	}
}

func TestWebValidation(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "192.0.2.1", "::1", "fd00::1", "2001:db8::1"} {
		for _, port := range []int{1, 3000, 65535} {
			config := Defaults()
			config.Web.Host = host
			config.Web.Port = port
			if err := config.Validate(); err != nil {
				t.Errorf("%s:%d: %v", host, port, err)
			}
		}
	}
	for _, host := range []string{"", "localhost", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "169.254.1.1", "fe80::1", "255.255.255.255", "127.0.0.1\nPORT=80"} {
		config := Defaults()
		config.Web.Host = host
		if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "web.host") {
			t.Errorf("host %q: %v", host, err)
		}
	}
	for _, port := range []int{-1, 0, 65536} {
		config := Defaults()
		config.Web.Port = port
		if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "web.port") {
			t.Errorf("port %d: %v", port, err)
		}
	}
}

func TestExporterURLIsLoopbackOrigin(t *testing.T) {
	for _, value := range []string{"http://127.0.0.1:9101", "http://[::1]:9101", ""} {
		config := Defaults()
		config.Web.ExporterURL = value
		if err := config.Validate(); err != nil {
			t.Fatal(value, err)
		}
	}
	for _, value := range []string{"http://example.com", "http://127.0.0.1:0", "http://user:pass@127.0.0.1", "https://127.0.0.1", "http://127.0.0.1/x", "http://127.0.0.1?secret=x"} {
		config := Defaults()
		config.Web.ExporterURL = value
		if config.Validate() == nil {
			t.Fatal("accepted", value)
		}
	}
}
