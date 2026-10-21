package selfupdate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"home-router/xray-core-helper/internal/testconfig"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"home-router/xray-core-helper/internal/bootstrap"
)

func localReleaseFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeLifecycleCandidate(t, root, true)
	hashes := map[string]string{}
	for _, name := range []string{"xray-core-helper", "xray-core-helperd", "domain-list-community"} {
		contents := make([]byte, 72)
		copy(contents, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
		binary.LittleEndian.PutUint16(contents[16:], 2)
		machine := uint16(62)
		if runtime.GOARCH == "arm64" {
			machine = 183
		}
		binary.LittleEndian.PutUint16(contents[18:], machine)
		copy(contents[64:], name)
		p := filepath.Join(root, "xray-core-helper/bin", name)
		if err := os.WriteFile(p, contents, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0755); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(contents)
		hashes[name] = hex.EncodeToString(hash[:])
	}
	m := Manifest{SchemaVersion: 1, Version: "helper-v2.0.0", Commit: strings.Repeat("a", 40), OS: "linux", Arch: runtime.GOARCH, Binaries: hashes, Dependencies: json.RawMessage(`{"domain-list-community":{"repository":"example.test","commit":"abc"}}`)}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	writeLifecycleFile(t, filepath.Join(root, "release.json"), string(raw))
	return root
}

func TestLocalBundleUpdateAndExplicitRollbackRestoreData(t *testing.T) {
	env := newLifecycleEnv(t)
	seedLifecycleInstalled(t, env)
	writeLifecycleFile(t, env.fs.Path(ManifestPath), `{"schemaVersion":1,"version":"helper-v1.0.0","commit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","os":"linux","arch":"amd64","binaries":{},"dependencies":{"domain-list-community":{"commit":"abc"}}}`)
	writeLifecycleFile(t, env.fs.Path("/usr/local/sbin/xray-core-helperd"), "old daemon")
	writeLifecycleFile(t, env.fs.Path("/etc/xray-core-helper/helper.env"), "XRAY_HELPER_WEB_ENABLED=1\n")
	writeLifecycleFile(t, env.e.InstallerSource, "saved installer")
	cfg := testconfig.Defaults()
	dbPath := env.fs.Path(cfg.Storage.Database)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE acceptance(value TEXT); INSERT INTO acceptance VALUES ('original')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	original, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	working := env.fs.Path("/etc/xray-core-helper/templConf/operator.txt")
	writeLifecycleFile(t, working, "operator working template")
	shipped := []string{"xray/config.json", "nftables/nftables.conf", "systemd/xray-tproxy-routing.service", "sysctl/99-xray.conf"}
	for _, name := range shipped {
		writeLifecycleFile(t, env.fs.Path("/etc/xray-core-helper/templConf/"+name), "previous template: "+name)
	}
	systemFiles := []string{"/usr/local/etc/xray/config.json", "/etc/nftables.conf", "/etc/sysctl.d/99-xray.conf", "/etc/systemd/system/xray-tproxy-routing.service", "/etc/systemd/system/xray.service.d/50-tproxy.conf"}
	for _, name := range systemFiles {
		writeLifecycleFile(t, env.fs.Path(name), "active system file: "+name)
	}
	admin := env.fs.Path("/etc/systemd/system/xray-core-helper.service.d/90-admin.conf")
	writeLifecycleFile(t, admin, "[Service]\nEnvironment=USER_CHOICE=1\n")
	env.runner.output = []byte("[Service]\nReadWritePaths=/usr/local/share/xray\n")
	candidate := localReleaseFixture(t)
	op, err := env.e.PrepareLocal(context.Background(), candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if op.Fresh || !op.Web || op.State != "prepared" {
		t.Fatalf("wrong operation: %#v", op)
	}
	originalSettings := readLifecycleFile(t, env.fs.Path(bootstrap.DefaultConfigPath))
	env.e.Check = func(context.Context, *Operation) error {
		if got := readLifecycleFile(t, env.fs.Path(bootstrap.DefaultTemplatePath)); got != lifecycleCandidateFiles["xray-core-helper/templConf/config.toml"] {
			t.Fatal("factory defaults not delivered")
		}
		if got := readLifecycleFile(t, env.fs.Path(bootstrap.DefaultConfigPath)); got != originalSettings {
			t.Fatal("user overrides changed")
		}
		if _, err := os.Stat(env.fs.Path("/etc/xray-core-helper/templConf/config.toml")); !os.IsNotExist(err) {
			t.Fatal("unexpected working copy of helper defaults", err)
		}
		for _, name := range shipped {
			path := env.fs.Path("/etc/xray-core-helper/templConf/" + name)
			if got := readLifecycleFile(t, path); got != lifecycleCandidateFiles["xray-core-helper/templConf/"+name] {
				t.Fatalf("template %s was not updated: %q", name, got)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("template %s metadata: %v %v", name, info, err)
			}
		}
		for _, name := range systemFiles {
			if got := readLifecycleFile(t, env.fs.Path(name)); got != "active system file: "+name {
				t.Fatalf("update changed active system file %s", name)
			}
		}
		writeLifecycleFile(t, dbPath, "simulated new incompatible database")
		return errors.New("verify_failed")
	}
	if err = env.e.Direct(context.Background(), op.ID, "apply"); err == nil {
		t.Fatal("verification failure accepted")
	}
	failed, _ := env.e.Store.Load()
	if failed.State != "failed" || !failed.SnapshotReady {
		t.Fatalf("recovery evidence: %#v", failed)
	}
	env.e.Check = func(context.Context, *Operation) error { return nil }
	if err = env.e.Direct(context.Background(), op.ID, "rollback"); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(dbPath)
	if string(restored) != string(original) {
		t.Fatal("rollback lost consistent database")
	}
	if got := readLifecycleFile(t, working); got != "operator working template" {
		t.Fatal(got)
	}
	if got := readLifecycleFile(t, admin); !strings.Contains(got, "USER_CHOICE") {
		t.Fatal("admin drop-in changed")
	}
	for _, name := range shipped {
		if got := readLifecycleFile(t, env.fs.Path("/etc/xray-core-helper/templConf/"+name)); got != "previous template: "+name {
			t.Fatalf("rollback did not restore template %s: %q", name, got)
		}
	}
	final, _ := env.e.Store.Load()
	if final.State != "rolled_back" {
		t.Fatal(final.State)
	}
	for _, call := range env.runner.calls {
		if strings.Contains(call, "xray.service") || strings.Contains(call, "xray-tproxy-routing.service") {
			t.Fatalf("unexpected data-plane action: %s", call)
		}
	}
}

func TestWebPreparationUsesDistinctEmptyNpmConfigurations(t *testing.T) {
	for _, failNpm := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "npm failure"}[failNpm], func(t *testing.T) {
			env := newLifecycleEnv(t)
			writeLifecycleFile(t, env.e.InstallerSource, "installer")
			env.runner.output = []byte("[Service]\nReadWritePaths=/usr/local/share/xray\n")
			if failNpm {
				env.runner.failWith = "/usr/bin/npm "
			}
			called := false
			env.runner.onCall = func(line string) {
				if !strings.HasPrefix(line, "/usr/bin/npm ") {
					return
				}
				called = true
				args := strings.Fields(line)
				configs := make(map[string]string)
				for i := 0; i+1 < len(args); i++ {
					if args[i] == "--userconfig" || args[i] == "--globalconfig" {
						configs[args[i]] = args[i+1]
					}
				}
				if len(configs) != 2 || configs["--userconfig"] == configs["--globalconfig"] {
					t.Fatalf("npm configuration paths must be distinct: %v", configs)
				}
				for _, path := range configs {
					if !strings.HasPrefix(path, env.e.Store.Root+string(os.PathSeparator)) {
						t.Fatalf("configuration outside private operation: %s", path)
					}
					info, err := os.Lstat(path)
					if err != nil || !info.Mode().IsRegular() || info.Size() != 0 || info.Mode().Perm() != 0600 {
						t.Fatalf("configuration must be an empty private regular file: %s (%v)", path, err)
					}
				}
			}
			op, err := env.e.PrepareLocal(context.Background(), localReleaseFixture(t), nil)
			if !called || (err != nil) != failNpm {
				t.Fatalf("called=%v err=%v", called, err)
			}
			if failNpm && (op == nil || op.State != "failed" || op.Error != "web_dependencies_failed") {
				t.Fatalf("missing failure evidence: %#v", op)
			}
			for _, call := range env.runner.calls {
				if strings.Contains(call, "systemctl") {
					t.Fatalf("preparation changed service state: %s", call)
				}
			}
		})
	}
}

func TestWebWaitsForHelperBeforeStarting(t *testing.T) {
	env := newLifecycleEnv(t)
	seedLifecycleInstalled(t, env)
	var helperChecked, webStarted atomic.Bool
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !webStarted.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer web.Close()
	cfg := testconfig.Defaults()
	cfg.Server.Socket = filepath.Join(t.TempDir(), "h.sock")
	host, port, err := net.SplitHostPort(strings.TrimPrefix(web.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Web.Host = host
	cfg.Web.Port, _ = strconv.Atoi(port)
	raw, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeLifecycleFile(t, env.fs.Path(bootstrap.DefaultConfigPath), string(raw))
	op := makeLifecycleOperation(t, env, "prepared", false, true)
	listener, err := net.Listen("unix", cfg.Server.Socket)
	if err != nil {
		t.Fatal(err)
	}
	helper := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		helperChecked.Store(true)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "setup_required", "version": op.TargetVersion + "+" + op.Commit[:12]})
	})}
	defer helper.Close()
	go helper.Serve(listener)
	env.runner.onCall = func(line string) {
		if strings.HasPrefix(line, "/usr/bin/systemctl start xrayui-web.service") {
			if !helperChecked.Load() {
				t.Error("web started before helper readiness; web.env may not exist yet")
			}
			webStarted.Store(true)
		}
	}
	env.e.Check = nil
	attempt := launchLifecycleAttempt(t, env.e, op, "apply")
	if err := env.e.Run(context.Background(), op.ID, attempt); err != nil {
		t.Fatal(err)
	}
	if !helperChecked.Load() || !webStarted.Load() {
		t.Fatal("helper/web verification was skipped")
	}
}

func TestWorkerLockLaunchHandoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.lock")
	release, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan error, 1)
	go func() {
		done, err := waitForWorkerLock(context.Background(), path)
		if err == nil {
			done()
		}
		acquired <- err
	}()
	select {
	case err = <-acquired:
		t.Fatalf("worker failed before handoff: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case err = <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("handoff did not finish")
	}
}

func TestPreflightRejectsFilesystemEvidenceWithoutDatabase(t *testing.T) {
	for _, pending := range []string{"/var/lib/xray-core-helper/setup/pending.json", "/usr/local/share/xray/.xray-core-helper-pending/manifest.json", "/usr/local/share/xray/.xray-core-helper-consolidation-pending/manifest.json"} {
		t.Run(filepath.Base(filepath.Dir(pending)), func(t *testing.T) {
			env := newLifecycleEnv(t)
			writeLifecycleFile(t, env.fs.Path(pending), "evidence")
			if err := env.e.Preflight(context.Background()); err == nil {
				t.Fatal("pending evidence ignored")
			}
		})
	}
}

func TestFreshHeadlessInstallsWithStandaloneSetupBeforeDaemon(t *testing.T) {
	env := newLifecycleEnv(t)
	writeLifecycleFile(t, env.e.InstallerSource, "saved installer")
	env.runner.output = []byte("[Service]\nReadWritePaths=/usr/local/share/xray\n")
	setupCalled := false
	env.e.Setup = func(context.Context) error {
		setupCalled = true
		if release, err := Lock(filepath.Join(env.e.Store.Root, "runtime.lock")); err == nil {
			release()
			t.Fatal("setup ran without exclusive ownership")
		}
		for _, call := range env.runner.calls {
			if strings.Contains(call, " start xray-core-helper.service") {
				t.Fatal("daemon started before standalone setup")
			}
		}
		return nil
	}
	web := false
	op, err := env.e.PrepareLocal(context.Background(), localReleaseFixture(t), &web)
	if err != nil {
		t.Fatal(err)
	}
	if !op.Fresh || op.Web {
		t.Fatalf("wrong fresh components: %#v", op)
	}
	if err = env.e.Direct(context.Background(), op.ID, "apply"); err != nil {
		t.Fatal(err)
	}
	if !setupCalled {
		t.Fatal("standalone setup omitted")
	}
	for _, call := range env.runner.calls {
		if strings.Contains(call, "npm") || strings.Contains(call, "xrayui-web") {
			t.Fatalf("headless web action: %s", call)
		}
	}
	if _, err = os.Stat(env.fs.Path("/opt/xrayui-web")); !os.IsNotExist(err) {
		t.Fatal("web installed on headless host")
	}
	final, _ := env.e.Store.Load()
	if final.State != "succeeded" {
		t.Fatal(final.State)
	}
}

func TestExplicitOlderAndSameLocalVersions(t *testing.T) {
	env := newLifecycleEnv(t)
	writeLifecycleFile(t, env.e.InstallerSource, "saved installer")
	writeLifecycleFile(t, env.fs.Path("/usr/local/sbin/xray-core-helperd"), "installed daemon")
	writeLifecycleFile(t, env.fs.Path("/etc/xray-core-helper/helper.env"), "XRAY_HELPER_WEB_ENABLED=0\n")
	writeLifecycleFile(t, env.fs.Path(ManifestPath), `{"schemaVersion":1,"version":"helper-v2.0.0","binaries":{},"minimum_installable_version":"helper-v1.0.0"}`)
	env.runner.output = []byte("[Service]\nReadWritePaths=/usr/local/share/xray\n")
	for _, version := range []string{"helper-v2.0.0", "helper-v1.0.0"} {
		root := localReleaseFixture(t)
		path := filepath.Join(root, "release.json")
		raw, _ := os.ReadFile(path)
		var m Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		m.Version = version
		raw, _ = json.Marshal(m)
		writeLifecycleFile(t, path, string(raw))
		op, err := env.e.PrepareLocal(context.Background(), root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = env.e.Direct(context.Background(), op.ID, "apply"); err != nil {
			t.Fatal(err)
		}
		installed, err := env.e.manifest()
		if err != nil || installed.Version != version {
			t.Fatalf("target=%s: %#v %v", version, installed, err)
		}
	}
}

func TestPrepareSettingsUsesBundleDefaultsBeforeFactoryExists(t *testing.T) {
	env := newLifecycleEnv(t)
	op := makeLifecycleOperation(t, env, "prepared", false, false)
	if err := os.Remove(env.fs.Path(bootstrap.DefaultTemplatePath)); err != nil {
		t.Fatal(err)
	}
	original := "# operator overrides\n[web]\nport = 3100\n"
	writeLifecycleFile(t, env.fs.Path(bootstrap.DefaultConfigPath), original)
	bundle := filepath.Join(env.e.Store.Dir(op.ID), "bundle")
	path, err := env.e.prepareSettings(op, bundle)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := bootstrap.LoadWithTemplate(path, filepath.Join(filepath.Dir(path), "defaults.toml"))
	if err != nil || cfg.Web.Port != 3100 {
		t.Fatalf("bundle overlay: %v %v", cfg.Web, err)
	}
	if readLifecycleFile(t, env.fs.Path(bootstrap.DefaultConfigPath)) != original {
		t.Fatal("preparation changed installed overrides")
	}
	if len(env.runner.calls) != 0 {
		t.Fatal("preparation invoked services")
	}
	// The restored old release has no factory template. Health verification must
	// still read its full settings without changing the restored installation.
	op.Attempts = append(op.Attempts, Attempt{Action: "rollback"})
	cfg, err = env.e.verificationConfig(op)
	if err != nil || cfg.Web.Port != 3100 {
		t.Fatalf("rollback verification: %v %v", cfg.Web, err)
	}
	op.Attempts[len(op.Attempts)-1].Action = "install"
	if _, err := env.e.verificationConfig(op); err == nil {
		t.Fatal("missing installed defaults silently accepted")
	}
}
