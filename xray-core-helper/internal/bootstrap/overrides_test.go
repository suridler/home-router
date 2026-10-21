package bootstrap

import (
	"bytes"
	"github.com/pelletier/go-toml/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUpdateOverridesPreservesOtherSectionsAndRemovesDefaults(t *testing.T) {
	raw := []byte("# operator config\n[web]\nport = 3100 # keep verbatim\ncontrol_enabled = false\n[logging]\nlevel = 'error'\naccess_path = '/var/log/xray/custom.log'\n")
	next, err := testDecode(raw)
	if err != nil {
		t.Fatal(err)
	}
	next.Logging = Defaults().Logging
	encoded, err := UpdateOverrides(raw, next, "logging")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte("port = 3100 # keep verbatim\ncontrol_enabled = false")) || bytes.Contains(encoded, []byte("[logging]")) || bytes.Contains(encoded, []byte("[network]")) {
		t.Fatalf("unrelated section rewritten or defaults materialized: %s", encoded)
	}
	loaded, err := testDecode(encoded)
	if err != nil || !reflect.DeepEqual(loaded, next) {
		t.Fatal("effective config changed", err)
	}
	again, err := UpdateOverrides(encoded, next, "logging")
	if err != nil || !bytes.Equal(again, encoded) {
		t.Fatal("save is not idempotent", err)
	}
}

func TestOverridesKeepExplicitEmptyAndFalse(t *testing.T) {
	next := Defaults()
	next.Web.ExporterURL = ""
	next.Web.ControlEnabled = false
	next.Geodata.Origins = []string{}
	next.Scheduler.GeodataUpdateEnabled = false
	encoded, err := UpdateOverrides(nil, next, "web", "geodata", "scheduler")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"exporter_url = ''", "control_enabled = false", "origins = []", "geodata_update_enabled = false"} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("missing override %s in %s", key, encoded)
		}
	}
	for _, key := range []string{"port =", "user_geosite =", "geodata_update_time =", "checksum"} {
		if strings.Contains(string(encoded), key) {
			t.Fatalf("default %s leaked into %s", key, encoded)
		}
	}
	loaded, err := testDecode(encoded)
	if err != nil || loaded.Web != next.Web || len(loaded.Geodata.Origins) != 0 || loaded.Scheduler != next.Scheduler {
		t.Fatal("empty/false override lost", err)
	}
}

func TestOverridesNestedSectionsAndTOMLForms(t *testing.T) {
	for _, source := range []string{
		"geodata = { max_asset_size_mb = 512 }\n[web]\nport = 3100 # keep\n",
		"geodata.max_asset_size_mb = 512\n[web]\nport = 3100 # keep\n",
		"['geodata']\nmax_asset_size_mb = 512\n[web]\nport = 3100 # keep\n",
		"[geodata]\nmax_asset_size_mb = 512\norigins = [\n'https://raw.githubusercontent.com',\n]\n[web]\nport = 3100 # keep\n",
	} {
		t.Run(source[:8], func(t *testing.T) {
			next, err := testDecode([]byte(source))
			if err != nil {
				t.Fatal(err)
			}
			next.Geodata.MaxAssetSizeMB = 256
			next.Geodata.Geosite.Checksum.URL = "https://raw.githubusercontent.com/example/checksum"
			encoded, err := UpdateOverrides([]byte(source), next, "geodata")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(encoded, []byte("port = 3100 # keep")) || bytes.Contains(encoded, []byte("max_asset_size_mb")) || bytes.Contains(encoded, []byte("algorithm")) {
				t.Fatalf("unexpected output: %s", encoded)
			}
			got, err := testDecode(encoded)
			if err != nil || !reflect.DeepEqual(got, next) {
				t.Fatal("nested override changed", err)
			}
		})
	}
}

func TestOverridesRejectInvalidInputWithoutResult(t *testing.T) {
	for _, raw := range []string{"[unknown]\nvalue=1\n", "[web]\nport=0\n", "[web]\nport=3000\nport=3001\n"} {
		if got, err := UpdateOverrides([]byte(raw), Defaults(), "web"); err == nil || got != nil {
			t.Fatal("invalid input accepted")
		}
	}
	if _, err := UpdateOverrides(nil, Defaults(), "unknown"); err == nil {
		t.Fatal("unknown section accepted")
	}
}

func TestTemplateUpdateChangesOnlyInheritedValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	templatePath := filepath.Join(dir, "defaults.toml")
	raw, err := os.ReadFile("../../templConf/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(templatePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(templatePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	initial := []byte("# preserve\n[web]\nport = 3100\n")
	if err := os.WriteFile(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadWithTemplate(path, templatePath)
	if err != nil {
		t.Fatal(err)
	}
	if config.Web.Port != 3100 || config.Backup.RetainPerTemplate != 10 {
		t.Fatal("bad overlay")
	}
	updated := bytes.Replace(raw, []byte("retain_per_template = 10"), []byte("retain_per_template = 20"), 1)
	updated = bytes.Replace(updated, []byte("port = 3000"), []byte("port = 3200"), 1)
	if err := os.WriteFile(templatePath, updated, 0600); err != nil {
		t.Fatal(err)
	}
	inherited, err := LoadWithTemplate(path, templatePath)
	if err != nil || inherited.Backup.RetainPerTemplate != 20 || inherited.Web.Port != 3100 {
		t.Fatal("template update ignored or override overwritten", err)
	}
	actual, _ := os.ReadFile(path)
	if !bytes.Equal(actual, initial) {
		t.Fatal("load rewrote user overrides")
	}
	// Saving the new default removes the override; a subsequent release can change it.
	inherited.Web.Port = 3200
	minimal, err := UpdateOverrides(actual, inherited, "web")
	if err != nil || bytes.Contains(minimal, []byte("port")) {
		t.Fatal("default remained pinned", err)
	}
	if err := os.WriteFile(path, minimal, 0600); err != nil {
		t.Fatal(err)
	}
	updated = bytes.Replace(updated, []byte("port = 3200"), []byte("port = 3300"), 1)
	if err := os.WriteFile(templatePath, updated, 0600); err != nil {
		t.Fatal(err)
	}
	inherited, err = LoadWithTemplate(path, templatePath)
	if err != nil || inherited.Web.Port != 3300 {
		t.Fatal("removed override did not follow new default", err)
	}
}

func TestNoFallbackForMissingOrIncompleteDefaultsTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWithTemplate(path, filepath.Join(filepath.Dir(path), "missing.toml")); err == nil {
		t.Fatal("missing defaults silently replaced with Go defaults")
	}
	raw, err := os.ReadFile("../../templConf/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	// Missing zero-valued settings must not silently acquire Go zero defaults.
	for _, omitted := range []string{"dns_log = false\n", "error_path = \"\"\n", "interface = \"\"\n"} {
		incomplete := bytes.Replace(raw, []byte(omitted), nil, 1)
		if _, err := parseDefaults(incomplete); err == nil || !strings.Contains(err.Error(), "incomplete helper defaults") {
			t.Fatalf("missing setting %q accepted: %v", omitted, err)
		}
	}
}

func TestEverySectionUsesSameOverridePersistence(t *testing.T) {
	cases := []struct {
		section string
		change  func(*Config)
	}{
		{"network", func(c *Config) { c.Network.InterceptMark = 2 }},
		{"logging", func(c *Config) { c.Logging.DNSLog = true }},
		{"scheduler", func(c *Config) { c.Scheduler.GeodataUpdateEnabled = false }},
		{"connectivity", func(c *Config) { c.Connectivity.TimeoutSeconds = 20 }},
		{"backup", func(c *Config) { c.Backup.RetainPerTemplate = 20 }},
		{"xray", func(c *Config) { c.Xray.API = "127.0.0.1:11085" }},
		{"geodata", func(c *Config) { c.Geodata.MaxAssetSizeMB = 512 }},
		{"storage", func(c *Config) { c.Storage.Database = "/tmp/custom.sqlite" }},
		{"server", func(c *Config) { c.Server.Socket = "/tmp/custom.sock" }},
		{"web", func(c *Config) { c.Web.ControlEnabled = false }},
	}
	for _, tc := range cases {
		t.Run(tc.section, func(t *testing.T) {
			next := Defaults()
			tc.change(&next)
			encoded, err := UpdateOverrides(nil, next, tc.section)
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]any
			if err := toml.Unmarshal(encoded, &values); err != nil || len(values) != 1 || values[tc.section] == nil {
				t.Fatalf("unexpected override: %s, %v", encoded, err)
			}
			restored, err := Decode(encoded, Defaults())
			if err != nil || !reflect.DeepEqual(restored, next) {
				t.Fatal("effective values differ", err)
			}
			cleared, err := UpdateOverrides(encoded, Defaults(), tc.section)
			if err != nil || len(bytes.TrimSpace(cleared)) != 0 {
				t.Fatalf("reset left overrides: %s, %v", cleared, err)
			}
		})
	}
}
