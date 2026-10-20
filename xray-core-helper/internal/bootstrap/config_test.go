package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAppliesDefaultsAndOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.toml")
	if err := os.WriteFile(path, []byte("[server]\nsocket = \"/tmp/helper.sock\"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Server.Socket != "/tmp/helper.sock" {
		t.Fatalf("server socket = %q", config.Server.Socket)
	}
	if config.Xray.Binary != "/usr/local/bin/xray" || config.Storage.Database != "/var/lib/xray-core-helper/helper.sqlite" {
		t.Fatalf("defaults were not applied: %#v", config)
	}
}

func TestGeodataSizeLimitConfig(t *testing.T) {
	for _, value := range []string{"", "max_asset_size_mb = 512", "max_asset_size_mb = 0", "max_asset_size_mb = -1"} {
		t.Run(value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "helper.toml")
			if err := os.WriteFile(path, []byte("[geodata]\n"+value+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			config, err := Load(path)
			bad := strings.Contains(value, "= 0") || strings.Contains(value, "= -1")
			if bad {
				if err == nil || !strings.Contains(err.Error(), "max_asset_size_mb") {
					t.Fatal("invalid limit accepted", err)
				}
				return
			}
			want := 256
			if value != "" {
				want = 512
			}
			if err != nil || config.Geodata.MaxAssetSizeMB != want {
				t.Fatal(config.Geodata.MaxAssetSizeMB, err)
			}
		})
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.toml")
	if err := os.WriteFile(path, []byte("[server]\nsocket = \"/tmp/helper.sock\"\nextra = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("Load() error = %v; want unknown-field error", err)
	}
}

func TestConfigPathCannotOverrideServiceThroughTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.toml")
	if err := os.WriteFile(path, []byte("[xray]\nconfig = \"/tmp/other.json\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("accepted independent xray.config setting")
	}
	if Defaults().Xray.Config != "" {
		t.Fatal("hardcoded Xray config fallback")
	}
}

func TestLoadRejectsRelativeFilesystemPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.toml")
	if err := os.WriteFile(path, []byte("[storage]\ndatabase = \"var/lib/helper.sqlite\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("Load() error = %v; want absolute-path error", err)
	}
}

func TestSocketGroupNameIsRestricted(t *testing.T) {
	for _, group := range []string{"xray-core-helper", "xray_helper", "_helper", "helper2"} {
		config := Defaults()
		config.Server.SocketGroup = group
		if err := config.Validate(); err != nil {
			t.Errorf("Validate() rejected group %q: %v", group, err)
		}
	}
	for _, group := range []string{"", "root:xray", "../helper", "Helper", "2helper", strings.Repeat("a", 33)} {
		config := Defaults()
		config.Server.SocketGroup = group
		if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "valid system group name") {
			t.Errorf("Validate() error for group %q = %v", group, err)
		}
	}
}

func TestGeodataDefaultsAreDisabledAndRequireSHA256(t *testing.T) {
	config := Defaults()
	if err := config.Validate(); err != nil {
		t.Fatalf("Defaults().Validate() error = %v", err)
	}
	if config.Geodata.Builder != "/usr/local/bin/domain-list-community" {
		t.Fatalf("geodata builder default = %q", config.Geodata.Builder)
	}
	if len(config.Geodata.Origins) != 0 ||
		config.Geodata.Geosite.URL != "" ||
		config.Geodata.Geoip.URL != "" {
		t.Fatalf("geodata defaults must not enable remote sources: %#v", config.Geodata)
	}
	for name, source := range map[string]GeodataSourceConfig{
		"geosite": config.Geodata.Geosite,
		"geoip":   config.Geodata.Geoip,
	} {
		if source.Checksum.Algorithm != "sha256" || source.Checksum.URL != "" {
			t.Errorf("%s checksum defaults = %#v; want mandatory sha256 policy and no source", name, source.Checksum)
		}
	}
}

func TestGeodataBuilderMustBeAnAbsoluteCleanPath(t *testing.T) {
	for _, builder := range []string{"", "domain-list-community", "../builder", "/usr/local/bin/../bin/builder"} {
		config := Defaults()
		config.Geodata.Builder = builder
		if err := config.Validate(); err == nil {
			t.Errorf("Validate() accepted geodata.builder %q", builder)
		}
	}
	config := Defaults()
	config.Geodata.Builder = "/opt/geodata/domain-list-community"
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() rejected absolute configured builder: %v", err)
	}
}

func TestGeodataAssetPathsAreRestricted(t *testing.T) {
	tests := []struct {
		name      string
		assetDir  string
		assetName string
		wantPath  string
		wantError bool
	}{
		{
			name:      "geosite in Xray directory",
			assetDir:  "/usr/local/share/xray",
			assetName: "geosite.dat",
			wantPath:  "/usr/local/share/xray/geosite.dat",
		},
		{
			name:      "geoip in helper directory",
			assetDir:  "/var/lib/xray-core-helper/geodata",
			assetName: "geoip.dat",
			wantPath:  "/var/lib/xray-core-helper/geodata/geoip.dat",
		},
		{
			name:      "arbitrary directory",
			assetDir:  "/tmp",
			assetName: "geosite.dat",
			wantError: true,
		},
		{
			name:      "nested arbitrary directory",
			assetDir:  "/usr/local/share/xray/other",
			assetName: "geoip.dat",
			wantError: true,
		},
		{
			name:      "path traversal asset name",
			assetDir:  "/usr/local/share/xray",
			assetName: "../geoip.dat",
			wantError: true,
		},
		{
			name:      "unknown asset name",
			assetDir:  "/usr/local/share/xray",
			assetName: "custom.dat",
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := Defaults()
			config.Xray.AssetDir = test.assetDir
			got, err := config.GeodataAssetPath(test.assetName)
			if test.wantError {
				if err == nil {
					t.Fatalf("GeodataAssetPath() = %q, nil; want error", got)
				}
				return
			}
			if err != nil || got != test.wantPath {
				t.Fatalf("GeodataAssetPath() = %q, %v; want %q", got, err, test.wantPath)
			}
		})
	}
}

func TestGeodataHTTPSOriginsAreStrict(t *testing.T) {
	tests := []struct {
		name      string
		origin    string
		wantError bool
	}{
		{name: "https origin", origin: "https://data.example"},
		{name: "https origin with port", origin: "https://data.example:8443"},
		{name: "http scheme", origin: "http://data.example", wantError: true},
		{name: "file scheme", origin: "file:///tmp", wantError: true},
		{name: "credentials", origin: "https://user:secret@data.example", wantError: true},
		{name: "query", origin: "https://data.example?token=x", wantError: true},
		{name: "empty query", origin: "https://data.example?", wantError: true},
		{name: "fragment", origin: "https://data.example#section", wantError: true},
		{name: "empty fragment", origin: "https://data.example#", wantError: true},
		{name: "path", origin: "https://data.example/assets", wantError: true},
		{name: "empty host", origin: "https://", wantError: true},
		{name: "invalid host", origin: "https://bad_host.example", wantError: true},
		{name: "empty port", origin: "https://data.example:", wantError: true},
		{name: "port out of range", origin: "https://data.example:65536", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := configuredGeodata()
			config.Origins = []string{test.origin}
			if !test.wantError {
				config.Geosite.URL = strings.Replace(config.Geosite.URL, "https://data.example", test.origin, 1)
				config.Geosite.Checksum.URL = strings.Replace(config.Geosite.Checksum.URL, "https://data.example", test.origin, 1)
				config.Geoip.URL = strings.Replace(config.Geoip.URL, "https://data.example", test.origin, 1)
				config.Geoip.Checksum.URL = strings.Replace(config.Geoip.Checksum.URL, "https://data.example", test.origin, 1)
			}
			err := config.Validate()
			if (err != nil) != test.wantError {
				t.Fatalf("Validate() error = %v, wantError %t", err, test.wantError)
			}
		})
	}

	t.Run("duplicate normalized origins", func(t *testing.T) {
		config := configuredGeodata()
		config.Origins = []string{"https://data.example", "https://DATA.example:443"}
		if err := config.Validate(); err == nil {
			t.Fatal("Validate() accepted duplicate normalized origins")
		}
	})
}

func TestGeodataRemoteURLsRequireConfiguredHTTPSOrigin(t *testing.T) {
	config := configuredGeodata()
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "allowed path", raw: "https://data.example/files/geosite.dat", want: true},
		{name: "allowed normalized default port", raw: "https://DATA.example:443/checksums/geoip", want: true},
		{name: "other origin redirect", raw: "https://redirect.example/geosite.dat"},
		{name: "http scheme", raw: "http://data.example/geosite.dat"},
		{name: "credentials", raw: "https://user:secret@data.example/geosite.dat"},
		{name: "query", raw: "https://data.example/geosite.dat?token=x"},
		{name: "empty query", raw: "https://data.example/geosite.dat?"},
		{name: "fragment", raw: "https://data.example/geosite.dat#part"},
		{name: "empty fragment", raw: "https://data.example/geosite.dat#"},
		{name: "missing path", raw: "https://data.example"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := config.AllowsRemoteURL(test.raw); got != test.want {
				t.Fatalf("AllowsRemoteURL(%q) = %t; want %t", test.raw, got, test.want)
			}
		})
	}
}

func TestGeodataRequiresChecksumMetadataForBothAssets(t *testing.T) {
	tests := []struct {
		name   string
		change func(*GeodataConfig)
	}{
		{
			name: "missing geosite checksum URL",
			change: func(config *GeodataConfig) {
				config.Geosite.Checksum.URL = ""
			},
		},
		{
			name: "missing geoip checksum URL",
			change: func(config *GeodataConfig) {
				config.Geoip.Checksum.URL = ""
			},
		},
		{
			name: "missing geosite asset URL",
			change: func(config *GeodataConfig) {
				config.Geosite.URL = ""
			},
		},
		{
			name: "missing geoip asset URL",
			change: func(config *GeodataConfig) {
				config.Geoip.URL = ""
			},
		},
		{
			name: "unsupported geosite checksum algorithm",
			change: func(config *GeodataConfig) {
				config.Geosite.Checksum.Algorithm = "md5"
			},
		},
		{
			name: "unsupported geoip checksum algorithm",
			change: func(config *GeodataConfig) {
				config.Geoip.Checksum.Algorithm = ""
			},
		},
		{
			name: "source origin missing",
			change: func(config *GeodataConfig) {
				config.Origins = nil
			},
		},
		{
			name: "origin without sources",
			change: func(config *GeodataConfig) {
				config.Geosite = defaultGeodataSource()
				config.Geoip = defaultGeodataSource()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := configuredGeodata()
			test.change(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("Validate() accepted incomplete or invalid checksum/source metadata")
			}
		})
	}
}

func TestLoadRejectsUnknownGeodataTOMLFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "unknown geodata field",
			body: "[geodata]\nunknown = true\n",
		},
		{
			name: "unknown source field",
			body: "[geodata.geosite]\nunknown = true\n",
		},
		{
			name: "unknown checksum field",
			body: "[geodata.geosite.checksum]\nunknown = true\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "helper.toml")
			if err := os.WriteFile(path, []byte(test.body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "strict mode") {
				t.Fatalf("Load() error = %v; want strict unknown-field error", err)
			}
		})
	}
}

func TestLoadRejectsDuplicateGeodataTOMLFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.toml")
	body := "[geodata]\norigins = [\"https://data.example\"]\norigins = [\"https://other.example\"]\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted a duplicate geodata TOML field")
	}
}

func TestGeodataValidationDoesNotMutateFilesystem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "helper.toml")
	contents := []byte("[server]\nsocket = \"/tmp/helper.sock\"\n")
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	beforeEntries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.GeodataAssetPath("geosite.dat"); err != nil {
		t.Fatal(err)
	}
	if config.Geodata.AllowsRemoteURL("https://data.example/geosite.dat") {
		t.Fatal("disabled defaults unexpectedly allowed a remote URL")
	}

	afterEntries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	afterContents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeEntries) != len(afterEntries) || beforeEntries[0].Name() != afterEntries[0].Name() {
		t.Fatalf("validation changed directory entries: before=%v after=%v", beforeEntries, afterEntries)
	}
	if string(afterContents) != string(contents) {
		t.Fatal("validation changed the input config file")
	}
}

func configuredGeodata() GeodataConfig {
	return GeodataConfig{
		Origins: []string{"https://data.example"},
		Geosite: GeodataSourceConfig{
			URL: "https://data.example/files/geosite.dat",
			Checksum: GeodataChecksumConfig{
				URL:       "https://data.example/checksums/geosite.sha256",
				Algorithm: "sha256",
			},
		},
		Geoip: GeodataSourceConfig{
			URL: "https://data.example/files/geoip.dat",
			Checksum: GeodataChecksumConfig{
				URL:       "https://data.example/checksums/geoip.sha256",
				Algorithm: "sha256",
			},
		},
	}
}

func TestServiceOwnedPathKeysAreNotTOMLSettings(t *testing.T) {
	for _, key := range []string{"binary", "asset_dir", "config"} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("[xray]\n"+key+" = \"/etc/other\"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("accepted independent service path", key)
		}
	}
}

func TestUserGeositeFilenameDefaultsAndValidation(t *testing.T) {
	if Defaults().Geodata.UserGeosite != "geosite_user.dat" {
		t.Fatal("wrong user geosite default")
	}
	for _, name := range []string{"geosite_user.dat", "geosite_custom-1.dat", "geosite_IR.dat"} {
		cfg := Defaults()
		cfg.Geodata.UserGeosite = name
		if err := cfg.Validate(); err != nil {
			t.Fatalf("valid name %q: %v", name, err)
		}
	}
	for _, name := range []string{"", "../geosite_user.dat", "/tmp/geosite_user.dat", "geosite.dat", "geoip_user.dat", "geosite_.dat", "geosite_user.dat.previous"} {
		cfg := Defaults()
		cfg.Geodata.UserGeosite = name
		if err := cfg.Validate(); err == nil {
			t.Fatalf("unsafe name accepted: %q", name)
		}
	}
}
