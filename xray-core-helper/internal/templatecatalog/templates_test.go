package templatecatalog

import (
	"context"
	"home-router/xray-core-helper/internal/testconfig"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/xrayconfig"
)

var forbiddenTemplateSecrets = []*regexp.Regexp{
	regexp.MustCompile(`(?i)vless://`),
	regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`),
	regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)"(privateKey|private_key|password|passwd|secretKey|secret_key|shortId|short_id)"\s*:`),
}

func TestRepositoryContainsEveryCatalogTemplate(t *testing.T) {
	root := repositoryTemplateRoot(t)
	for _, entry := range Default().Entries() {
		relative := strings.TrimPrefix(entry.WorkingPath, WorkingRoot+"/")
		if relative == entry.WorkingPath || relative == "" {
			t.Fatalf("working path %q is not below %q", entry.WorkingPath, WorkingRoot)
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Errorf("template %q: %v", entry.ID, err)
			continue
		}
		if !info.Mode().IsRegular() {
			t.Errorf("template %q is not a regular file", entry.ID)
		}
	}
}

func TestRepositoryTemplatesContainNoCredentialMaterial(t *testing.T) {
	root := repositoryTemplateRoot(t)
	for _, entry := range Default().Entries() {
		relative := strings.TrimPrefix(entry.WorkingPath, WorkingRoot+"/")
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		for _, pattern := range forbiddenTemplateSecrets {
			if pattern.Match(contents) {
				t.Errorf("template %q contains credential-like material matching %q", entry.ID, pattern)
			}
		}
	}
}

func TestXrayRepositoryTemplateRendersParseableJSONC(t *testing.T) {
	path := filepath.Join(repositoryTemplateRoot(t), "xray", "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	config := testconfig.Defaults()
	config.Network.Interface, config.Network.Address, config.Network.IPv4Prefix = "br-test", "192.0.2.1", "192.0.2.0/24"
	rendered, err := xrayconfig.RenderSetupTemplate(raw, nil, xrayconfig.TemplateValues{Network: config.Network, Logging: config.Logging, API: config.Xray.API, UserGeosite: config.Geodata.UserGeosite, UserGeoip: config.Geodata.UserGeoip})
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "candidate.json")
	if err = os.WriteFile(path, rendered, 0600); err != nil {
		t.Fatal(err)
	}
	reader := xrayconfig.NewReader(xrayconfig.Settings{Config: path})
	status := reader.Status(context.Background())
	if !status.ConfigReadable || !status.ConfigParsed || status.ConfigState != "parsed" {
		t.Fatalf("Xray template status = %#v", status)
	}
}

func TestWebIsNotANativeTemplate(t *testing.T) {
	if _, err := Default().Lookup(ID("web-config")); err == nil {
		t.Fatal("retired web template accepted")
	}
	if _, err := os.Stat(filepath.Join(repositoryTemplateRoot(t), "web", "xrayui-web.env")); !os.IsNotExist(err) {
		t.Fatal("web template must not be shipped")
	}
}

func repositoryTemplateRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "templConf"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
