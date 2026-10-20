package packaging

import (
	"fmt"

	"home-router/xray-core-helper/internal/filemetadata"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type templateAsset struct {
	directory string
	name      string
}

var templateAssets = []templateAsset{
	{directory: "xray", name: "config.json"},
	{directory: "nftables", name: "nftables.conf"},
	{directory: "systemd", name: "xray-tproxy-routing.service"},
	{directory: "sysctl", name: "99-xray.conf"},
}

func TestTemplateTmpfilesContract(t *testing.T) {
	metadata := filemetadata.Working()
	if metadata.Mode != 0600 || metadata.UID != 0 || metadata.GID != 0 {
		t.Fatal("setup working metadata differs from tmpfiles root:root 0600 policy")
	}
	config := readAsset(t, "tmpfiles.d", "xray-core-helper.conf")
	for _, asset := range templateAssets {
		relative := filepath.ToSlash(filepath.Join(asset.directory, asset.name))
		want := fmt.Sprintf(
			"C /etc/xray-core-helper/templConf/%s 0600 root root - /usr/share/xray-core-helper/templConf/%s",
			relative,
			relative,
		)
		if !hasLine(config, want) {
			t.Errorf("tmpfiles config is missing %q", want)
		}
	}
	if strings.Contains(config, "C+") {
		t.Fatal("tmpfiles config must never replace an existing working template")
	}
}

func TestTemplateTmpfilesFirstInstallAndRepeatAreSafe(t *testing.T) {
	root := t.TempDir()
	uid, gid := os.Getuid(), os.Getgid()
	configPath := writeTestTmpfilesConfig(t, root, uid, gid)
	factoryRoot := filepath.Join(root, "usr", "share", "xray-core-helper", "templConf")
	workingRoot := filepath.Join(root, "etc", "xray-core-helper", "templConf")

	for _, asset := range templateAssets {
		source := filepath.Join("..", "templConf", asset.directory, asset.name)
		contents, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		writeTemplateFixture(t, filepath.Join(factoryRoot, asset.directory, asset.name), contents, 0644)
	}

	runTmpfiles(t, root, configPath)
	assertInstalledTemplates(t, factoryRoot, workingRoot, uid, gid)

	preserved := filepath.Join(workingRoot, templateAssets[0].directory, templateAssets[0].name)
	if err := os.WriteFile(preserved, []byte("operator working copy\n"), 0600); err != nil {
		t.Fatal(err)
	}
	factoryChanged := filepath.Join(factoryRoot, templateAssets[0].directory, templateAssets[0].name)
	if err := os.WriteFile(factoryChanged, []byte("new packaged default\n"), 0644); err != nil {
		t.Fatal(err)
	}

	runTmpfiles(t, root, configPath)
	contents, err := os.ReadFile(preserved)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "operator working copy\n" {
		t.Fatalf("repeat install replaced working template: %q", contents)
	}
	assertInstalledMetadata(t, preserved, 0600, uid, gid)
}

func writeTestTmpfilesConfig(t *testing.T, root string, uid, gid int) string {
	t.Helper()
	production := readAsset(t, "tmpfiles.d", "xray-core-helper.conf")
	lines := make([]string, 0, len(templateAssets)*2+1)
	owner := fmt.Sprintf(" %d %d ", uid, gid)
	for _, line := range strings.Split(production, "\n") {
		if !strings.Contains(line, "/etc/xray-core-helper/templConf") {
			continue
		}
		lines = append(lines, strings.Replace(line, " root root ", owner, 1))
	}
	path := filepath.Join(root, "template-test.conf")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runTmpfiles(t *testing.T, root, configPath string) {
	t.Helper()
	command := exec.Command("systemd-tmpfiles", "--root="+root, "--create", configPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("systemd-tmpfiles: %v: %s", err, output)
	}
}

func assertInstalledTemplates(t *testing.T, factoryRoot, workingRoot string, uid, gid int) {
	t.Helper()
	assertInstalledMetadata(t, workingRoot, 0700, uid, gid)
	for _, asset := range templateAssets {
		directory := filepath.Join(workingRoot, asset.directory)
		assertInstalledMetadata(t, directory, 0700, uid, gid)
		installed := filepath.Join(directory, asset.name)
		assertInstalledMetadata(t, installed, 0600, uid, gid)

		got, err := os.ReadFile(installed)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(factoryRoot, asset.directory, asset.name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("installed %s content differs from packaged default", asset.name)
		}
	}
}

func assertInstalledMetadata(t *testing.T, path string, mode os.FileMode, uid, gid int) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("%s mode = %o; want %o", path, info.Mode().Perm(), mode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s has unsupported stat metadata", path)
	}
	if int(stat.Uid) != uid || int(stat.Gid) != gid {
		t.Errorf("%s owner = %d:%d; want %d:%d", path, stat.Uid, stat.Gid, uid, gid)
	}
}

func writeTemplateFixture(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}
