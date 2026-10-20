package xrayconfig

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/usergeoip"
)

// Compile the complete shipped policy with synthetic categories only. No host
// config, service, network listeners or external DNS is used by run -test.
func TestShippedTemplateWithXray(t *testing.T) {
	executable := os.Getenv("XRAY_HELPER_TEST_XRAY_BINARY")
	if executable == "" {
		t.Skip("set XRAY_HELPER_TEST_XRAY_BINARY to a disposable Xray executable copy")
	}
	if !filepath.IsAbs(executable) {
		t.Fatal("Xray fixture binary must be absolute")
	}
	for _, populated := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "populated"}[populated], func(t *testing.T) {
			dir := t.TempDir()
			raw, err := os.ReadFile("../../templConf/xray/config.json")
			if err != nil {
				t.Fatal(err)
			}
			values := templateTestValues()
			values.UserGeosite, values.UserGeoip = "geosite_custom.dat", "geoip_custom.dat"
			rendered, err := RenderSetupTemplate(raw, nil, values)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := parseSource(rendered)
			if err != nil {
				t.Fatal(err)
			}
			categories := map[string]map[string]bool{}
			var collect func(any)
			collect = func(value any) {
				switch v := value.(type) {
				case map[string]any:
					for _, item := range v {
						collect(item)
					}
				case []any:
					for _, item := range v {
						collect(item)
					}
				case string:
					var name, category string
					if strings.HasPrefix(v, "geosite:") {
						name, category = "geosite.dat", strings.TrimPrefix(v, "geosite:")
					}
					if strings.HasPrefix(v, "geoip:") {
						name, category = "geoip.dat", strings.TrimPrefix(v, "geoip:")
					}
					if strings.HasPrefix(v, "ext:") {
						parts := strings.Split(v, ":")
						if len(parts) == 3 {
							name, category = parts[1], parts[2]
						}
					}
					if name != "" {
						if categories[name] == nil {
							categories[name] = map[string]bool{}
						}
						categories[name][strings.ToUpper(category)] = true
					}
				}
			}
			collect(parsed.value)
			// Both reference bindings must survive when filenames differ from defaults.
			if len(categories) != 4 || !categories[values.UserGeosite]["USERLIST"] || !categories[values.UserGeoip]["USERLIST"] {
				t.Fatal("asset references missing")
			}
			field := func(dst []byte, tag byte, payload []byte) []byte {
				dst = append(dst, tag)
				dst = binary.AppendUvarint(dst, uint64(len(payload)))
				return append(dst, payload...)
			}
			for name, names := range categories {
				var asset []byte
				for category := range names {
					entry := field(nil, 0x0a, []byte(category))
					if name == "geosite.dat" || name == values.UserGeosite {
						hostname := "example.test"
						if !populated && name == values.UserGeosite {
							hostname = "xray-core-helper-empty-domain.invalid"
						}
						domain := field([]byte{0x08, 0x02}, 0x12, []byte(hostname))
						entry = field(entry, 0x12, domain)
					} else if name == "geoip.dat" {
						cidr := field(nil, 0x0a, []byte{192, 0, 2, 99})
						cidr = append(cidr, 0x10, 0x20)
						entry = field(entry, 0x12, cidr)
					}
					asset = field(asset, 0x0a, entry)
				}
				if populated && name == values.UserGeoip {
					asset, err = usergeoip.Encode([]string{"192.0.2.7"})
					if err != nil {
						t.Fatal(err)
					}
				}
				if err = os.WriteFile(filepath.Join(dir, name), asset, 0600); err != nil {
					t.Fatal(err)
				}
			}
			config := filepath.Join(dir, "xray.json")
			if err = os.WriteFile(config, rendered, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "run", "-test", "-config", config)
			command.Env = []string{"XRAY_LOCATION_ASSET=" + dir}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("Xray rejected shipped template: %v\n%s", err, output)
			}
			unchanged, err := os.ReadFile("../../templConf/xray/config.json")
			if err != nil || string(unchanged) != string(raw) {
				t.Fatal("compiler preparation modified template", err)
			}
		})
	}
}
