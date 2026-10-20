package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run the actual headless branch with every privileged command replaced by a
// recording stub. All config paths and the helper tree are isolated fixtures.
func TestSourceHeadlessBundleDoesNotNeedWebTreeNodeOrWebIdentity(t *testing.T) {
	testHeadlessBundle(t, false)
}

// The prebuilt entry no longer performs installation in shell. It must hand
// off every option to the verified bundle CLI without touching host commands.
func TestPrebuiltDelegatesStandaloneInstaller(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "helper")
	if err := os.MkdirAll(filepath.Join(helper, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(helper, "packaging"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(helper, "packaging/verify-release.py"), []byte("# bundle verification has separate fixture coverage\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "install-bundle.sh")
	raw := readAsset(t, "install-bundle.sh")
	raw = strings.Replace(raw, "[[ ${EUID} -eq 0 ]] || { echo \"run with sudo\" >&2; exit 1; }", ": # hermetic", 1)
	start := strings.Index(raw, "helper_root=")
	end := start + strings.Index(raw[start:], "\n")
	raw = raw[:start] + "helper_root=" + helper + raw[end:]
	if err := os.WriteFile(script, []byte(raw), 0700); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(helper, "bin/xray-core-helper")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nexit 37\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, "--without-web", "--prebuilt")
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 37 {
		t.Fatalf("standalone result lost: %s %v", out, err)
	}
	expected := "install\n--bundle\n" + helper + "/..\n--without-web\n"
	if string(out) != expected {
		t.Fatalf("wrong delegation %q", out)
	}
}

func testHeadlessBundle(t *testing.T, prebuilt bool) {
	raw, err := os.ReadFile("install-bundle.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	fixture := filepath.Join(root, "helper")
	bin := filepath.Join(root, "bin")
	log := filepath.Join(root, "calls")
	for _, dir := range []string{bin, filepath.Join(fixture, "packaging")} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(fixture, "config.example.toml"), []byte("[web]\nhost = \"127.0.0.1\"\nport = 3000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(fixture, "packaging/install-templconf.sh"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(fixture, "packaging/dlc_builder.py"), []byte("import pathlib,sys\npathlib.Path(sys.argv[1]).write_bytes(b'fixture builder')\npathlib.Path(sys.argv[2]).write_text('fixture license')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stub := []byte("#!/bin/sh\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$XCH_TEST_CALLS\"\n")
	for _, name := range []string{"go", "systemd-sysusers", "systemd-tmpfiles", "systemd-analyze", "systemctl", "install", "node", "npm"} {
		if err = os.WriteFile(filepath.Join(bin, name), stub, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(bin, "systemctl"), append(append([]byte{}, stub...), []byte(`
case "$*" in
 "is-active --quiet xray-core-helper-routing.service"|"is-active --quiet xray-core-helper-errors.service"|"is-active --quiet update-xray-geodata.service") exit 3;;
esac
`)...), 0700); err != nil {
		t.Fatal(err)
	}
	for _, stem := range []string{"xray-core-helper-routing", "xray-core-helper-errors", "update-xray-geodata"} {
		dir := filepath.Join(root, "host/etc/systemd/system")
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, ext := range []string{".timer", ".service"} {
			if err = os.WriteFile(filepath.Join(dir, stem+ext), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	source := strings.Replace(string(raw), "[[ ${EUID} -eq 0 ]] || { echo \"run with sudo\" >&2; exit 1; }", ": # isolated unprivileged test", 1)
	start := strings.Index(source, "helper_root=")
	end := strings.Index(source[start:], "\n") + start
	source = source[:start] + "helper_root=" + fixture + source[end:]
	source = strings.ReplaceAll(source, "/etc/", root+"/host/etc/")
	if err = os.WriteFile(filepath.Join(bin, "go"), append(append([]byte{}, stub...), []byte(`
while [ "$#" -gt 0 ]; do
 if [ "$1" = -o ]; then shift; printf '#!/bin/sh\nexit 0\n' > "$1"; chmod 700 "$1"; break; fi
 shift
done
`)...), 0700); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(bin, "helper-cli")
	if err = os.WriteFile(cli, []byte("#!/bin/sh\nprintf '%s\\n' '{\"socket\":\"/run/test-helper.sock\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	source = strings.ReplaceAll(source, `"$build_dir/xray-core-helper" discover-xray`, cli+" discover-xray")
	source = strings.ReplaceAll(source, `"$build_dir/xray-core-helper" -config`, cli+" -config")
	source = strings.ReplaceAll(source, "/usr/local/bin/xray-core-helper", cli)
	installer := string(stub) + `
last=""; previous=""
for arg in "$@"; do previous=$last; last=$arg; done
case " $* " in
 *" -d "*) mkdir -p -- "$last";;
 *) mkdir -p -- "$(dirname -- "$last")"; if [ -f "$previous" ]; then cp -- "$previous" "$last"; else : > "$last"; fi;;
esac
`
	if err = os.WriteFile(filepath.Join(bin, "install"), []byte(installer), 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"--without-web"}
	if prebuilt {
		cliFixture, readErr := os.ReadFile(cli)
		if readErr != nil {
			t.Fatal(readErr)
		}
		args = append(args, "--prebuilt")
		if err = os.MkdirAll(filepath.Join(fixture, "bin"), 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"xray-core-helper", "xray-core-helperd", "domain-list-community"} {
			if err = os.WriteFile(filepath.Join(fixture, "bin", name), cliFixture, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err = os.MkdirAll(filepath.Join(fixture, "licenses"), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(fixture, "licenses/domain-list-community.txt"), []byte("fixture license"), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(fixture, "packaging", "verify-release.py"), []byte("# separate verifier fixture tests cover validation\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(bin, "go"), append(stub, []byte("exit 99\n")...), 0700); err != nil {
			t.Fatal(err)
		}
	}
	source = strings.ReplaceAll(source, "/usr/local/", root+"/host/usr/local/")
	source = strings.ReplaceAll(source, "/var/", root+"/host/var/")
	script := filepath.Join(root, "install.sh")
	if err = os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "XRAY_HELPER_WEB_ENABLED=1", "XCH_TEST_CALLS="+log)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed: %v %s", err, output)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if prebuilt && strings.Contains(string(calls), "go ") {
		t.Fatal("prebuilt install invoked Go", string(calls))
	}
	if _, err = os.Stat(filepath.Join(root, "host/usr/local/bin/domain-list-community")); err != nil {
		t.Fatal("geosite builder not installed", err)
	}
	for _, forbidden := range []string{"node ", "npm ", "xrayui-web.conf", "/opt/xrayui-web", "enable --now xrayui-web"} {
		if strings.Contains(string(calls), forbidden) {
			t.Fatalf("headless dependency %q in %s", forbidden, calls)
		}
	}
	if !strings.Contains(string(calls), "systemctl restart xray-core-helper.service") || !strings.Contains(string(output), "Headless mode") {
		t.Fatalf("headless daemon not started: %s %s", calls, output)
	}
	for _, stem := range []string{"xray-core-helper-routing", "xray-core-helper-errors", "update-xray-geodata"} {
		if !strings.Contains(string(calls), "systemctl disable --now "+stem+".timer") {
			t.Fatal("former timer not disabled", string(calls))
		}
		for _, ext := range []string{".timer", ".service"} {
			if _, err := os.Stat(filepath.Join(root, "host/etc/systemd/system", stem+ext)); !os.IsNotExist(err) {
				t.Fatal("former unit remains", stem, ext, err)
			}
		}
	}
	// An in-flight former job must not be interrupted or replaced by an upgrade.
	if err = os.WriteFile(cli, []byte("#!/bin/sh\nprintf '%s\\n' '{\"socket\":\"/run/test-helper.sock\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "systemctl"), stub, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(log, nil, 0600); err != nil {
		t.Fatal(err)
	}
	busy := exec.Command("bash", append([]string{script}, args...)...)
	busy.Env = cmd.Env
	if output, err = busy.CombinedOutput(); err == nil || !strings.Contains(string(output), "still running") {
		t.Fatal("active maintenance job accepted", string(output), err)
	}
	busyCalls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"install ", "systemctl restart", "systemctl stop"} {
		if strings.Contains(string(busyCalls), forbidden) {
			t.Fatal("active operation interrupted", string(busyCalls))
		}
	}
	// A discovery failure must stop before any installed-file/service mutation.
	if err = os.WriteFile(cli, []byte("#!/bin/sh\nexit 42\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(log, nil, 0600); err != nil {
		t.Fatal(err)
	}
	retry := exec.Command("bash", append([]string{script}, args...)...)
	retry.Env = cmd.Env
	if output, err = retry.CombinedOutput(); err == nil {
		t.Fatal("discovery failure accepted", string(output))
	}
	calls, err = os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"install ", "systemd-sysusers ", "systemctl restart"} {
		if strings.Contains(string(calls), forbidden) {
			t.Fatal("mutation after failed discovery", string(calls))
		}
	}

}
