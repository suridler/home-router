package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"home-router/xray-core-helper/internal/bootstrap"
)

type lifecycleRunner struct {
	calls    []string
	output   []byte
	failWith string
	onCall   func(string)
}

func (r *lifecycleRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, line)
	if r.onCall != nil {
		r.onCall(line)
	}
	if r.failWith != "" && strings.Contains(line, r.failWith) {
		return nil, errors.New("synthetic command failure")
	}
	return r.output, nil
}

type lifecycleEnv struct {
	e      *Engine
	fs     Files
	runner *lifecycleRunner
}

func newLifecycleEnv(t *testing.T) *lifecycleEnv {
	t.Helper()
	fs := Files{SystemRoot: t.TempDir()}
	cfg := bootstrap.Defaults()
	raw, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal bootstrap defaults: %v", err)
	}
	writeLifecycleFile(t, fs.Path(bootstrap.DefaultConfigPath), string(raw))
	runner := &lifecycleRunner{}
	e := &Engine{
		Store:           NewStore(filepath.Join(t.TempDir(), "state")),
		Files:           fs,
		Runner:          runner,
		ConfigPath:      bootstrap.DefaultConfigPath,
		InstallerSource: filepath.Join(fs.SystemRoot, "installer-source"),
		Check:           func(context.Context, *Operation) error { return nil },
	}
	return &lifecycleEnv{e: e, fs: fs, runner: runner}
}

func writeLifecycleFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readLifecycleFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func installLifecycleInstaller(t *testing.T, e *Engine) string {
	t.Helper()
	content := []byte("trusted test installer\n")
	if err := atomicWrite(filepath.Join(e.Store.Root, "installer"), content, 0700); err != nil {
		t.Fatalf("write installer: %v", err)
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

var lifecycleCandidateFiles = map[string]string{
	"xray-core-helper/templConf/xray/config.json":                            "new-xray-template\n",
	"xray-core-helper/templConf/nftables/nftables.conf":                      "new-nft-template\n",
	"xray-core-helper/templConf/systemd/xray-tproxy-routing.service":         "new-routing-template\n",
	"xray-core-helper/templConf/sysctl/99-xray.conf":                         "new-sysctl-template\n",
	"xray-core-helper/bin/xray-core-helper":                                  "new-helper\n",
	"xray-core-helper/bin/xray-core-helperd":                                 "new-helperd\n",
	"xray-core-helper/bin/domain-list-community":                             "new-domain-list\n",
	"xray-core-helper/licenses/domain-list-community.txt":                    "new-license\n",
	"xray-core-helper/templConf/new.toml":                                    "new-template\n",
	"release.json":                                                           `{"schemaVersion":1,"version":"helper-v2.0.0","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","os":"linux","arch":"amd64","binaries":{}}` + "\n",
	"xray-core-helper/packaging/sysusers.d/xray-core-helper.conf":            "new-sysusers\n",
	"xray-core-helper/packaging/tmpfiles.d/xray-core-helper.conf":            "new-tmpfiles\n",
	"xray-core-helper/packaging/systemd/50-tproxy.conf":                      "new-native-dropin\n",
	"xray-core-helper/packaging/systemd/xray-core-helper.service":            "new-helper-unit\n",
	"xray-core-helper/packaging/systemd/xray-core-helper-nft-commit.service": "new-nft-commit-unit\n",
	"xray-core-helper/packaging/systemd/xray-core-helper-sysctl.service":     "new-sysctl-unit\n",
	"xray-core-helper/packaging/systemd/xray-core-helper-rollback.service":   "new-rollback-unit\n",
	"xray-core-helper/packaging/systemd/xray-core-helper-rollback.timer":     "new-rollback-timer\n",
}

func writeLifecycleCandidate(t *testing.T, bundle string, web bool) {
	t.Helper()
	for name, body := range lifecycleCandidateFiles {
		writeLifecycleFile(t, filepath.Join(bundle, name), body)
	}
	if web {
		for name, body := range map[string]string{
			"xrayui-web/index.html":                           "new-web\n",
			"xrayui-web/xrayui-web.service":                   "new-web-unit\n",
			"xrayui-web/packaging/sysusers.d/xrayui-web.conf": "new-web-sysusers\n",
		} {
			writeLifecycleFile(t, filepath.Join(bundle, name), body)
		}
	}
}

func makeLifecycleOperation(t *testing.T, env *lifecycleEnv, state string, fresh, web bool) *Operation {
	t.Helper()
	op := &Operation{
		SchemaVersion: 1,
		ID:            ID(),
		SourceVersion: "helper-v1.0.0",
		TargetVersion: "helper-v2.0.0",
		Commit:        strings.Repeat("a", 40),
		Arch:          runtime.GOARCH,
		Fresh:         fresh,
		Web:           web,
		State:         state,
		ConfigPath:    env.e.ConfigPath,
		CreatedAt:     time.Now().UTC(),
	}
	bundle := filepath.Join(env.e.Store.Dir(op.ID), "bundle")
	writeLifecycleCandidate(t, bundle, web)
	sum, err := digest(bundle)
	if err != nil {
		t.Fatalf("digest candidate: %v", err)
	}
	op.CandidateDigest = sum
	op.InstallerDigest = installLifecycleInstaller(t, env.e)
	paths := []byte("[Service]\nReadOnlyPaths=/opt/xray\n")
	writeLifecycleFile(t, filepath.Join(env.e.Store.Dir(op.ID), "installation-paths.conf"), string(paths))
	pathsHash := sha256.Sum256(paths)
	op.InstallationPathsDigest = hex.EncodeToString(pathsHash[:])
	if err := env.e.Store.Save(op); err != nil {
		t.Fatalf("save operation: %v", err)
	}
	return op
}

func seedLifecycleInstalled(t *testing.T, env *lifecycleEnv) {
	t.Helper()
	for path, body := range map[string]string{
		"/usr/local/bin/xray-core-helper":                                      "old-helper\n",
		"/usr/local/bin/xray-core-helperd":                                     "old-helperd\n",
		"/usr/local/bin/domain-list-community":                                 "old-domain-list\n",
		"/usr/local/share/licenses/xray-core-helper/domain-list-community.txt": "old-license\n",
		"/usr/share/xray-core-helper/templConf/old.toml":                       "old-template\n",
		"/etc/sysusers.d/xray-core-helper.conf":                                "old-sysusers\n",
		"/etc/systemd/system/xray-core-helper.service":                         "old-helper-unit\n",
		"/etc/xray-core-helper/helper.env":                                     "XRAY_HELPER_WEB_ENABLED=0\n",
		ManifestPath:                                                           "old-manifest\n",
		"/var/lib/xray-core-helper/legacy.txt":                                 "old-state\n",
	} {
		writeLifecycleFile(t, env.fs.Path(path), body)
	}
}

func currentBootID(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatalf("read boot id: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

func launchLifecycleAttempt(t *testing.T, e *Engine, op *Operation, action string) string {
	t.Helper()
	id := ID()
	op.Attempts = append(op.Attempts, Attempt{
		ID:        id,
		Action:    action,
		State:     "launching",
		Stage:     "launching",
		BootID:    currentBootID(t),
		StartedAt: time.Now().UTC(),
	})
	op.State = "launching"
	op.Set("launching")
	if err := e.Store.Save(op); err != nil {
		t.Fatalf("save attempt: %v", err)
	}
	return id
}

func TestRequestActionIdempotencyAndConflict(t *testing.T) {
	env := newLifecycleEnv(t)
	op := makeLifecycleOperation(t, env, "prepared", false, false)
	actionID := ID()

	first, err := env.e.Request(context.Background(), op.ID, "apply", actionID)
	if err != nil {
		t.Fatalf("first apply request: %v", err)
	}
	if first.State != "launching" || len(first.Attempts) != 1 {
		t.Fatalf("unexpected launched operation: state=%q attempts=%d", first.State, len(first.Attempts))
	}
	if len(env.runner.calls) != 1 || !strings.Contains(env.runner.calls[0], "/usr/bin/systemd-run") ||
		!strings.Contains(env.runner.calls[0], "--worker "+op.ID) || !strings.Contains(env.runner.calls[0], "--attempt "+actionID) {
		t.Fatalf("unexpected launch command: %v", env.runner.calls)
	}

	repeated, err := env.e.Request(context.Background(), op.ID, "apply", actionID)
	if err != nil {
		t.Fatalf("repeated apply request: %v", err)
	}
	if repeated.ID != first.ID || repeated.State != "launching" || len(repeated.Attempts) != 1 {
		t.Fatalf("idempotent request changed operation: %+v", repeated)
	}
	if len(env.runner.calls) != 1 {
		t.Fatalf("idempotent request launched another worker: %v", env.runner.calls)
	}

	if _, err = env.e.Request(context.Background(), op.ID, "retry", actionID); err == nil ||
		!strings.Contains(err.Error(), "action_id_conflict") {
		t.Fatalf("conflicting action id accepted: %v", err)
	}
	if len(env.runner.calls) != 1 {
		t.Fatalf("conflicting action launched a worker: %v", env.runner.calls)
	}
}

func TestRequestStateAndInputGuards(t *testing.T) {
	t.Run("invalid ids and action", func(t *testing.T) {
		env := newLifecycleEnv(t)
		op := makeLifecycleOperation(t, env, "prepared", false, false)
		if _, err := env.e.Request(context.Background(), "not-an-id", "apply", ID()); err == nil {
			t.Fatal("invalid operation id accepted")
		}
		if _, err := env.e.Request(context.Background(), op.ID, "apply", "not-an-id"); err == nil {
			t.Fatal("invalid action id accepted")
		}
		if _, err := env.e.Request(context.Background(), op.ID, "upgrade", ID()); err == nil {
			t.Fatal("unknown action accepted")
		}
		if len(env.runner.calls) != 0 {
			t.Fatalf("rejected requests launched commands: %v", env.runner.calls)
		}
	})

	t.Run("apply requires prepared", func(t *testing.T) {
		env := newLifecycleEnv(t)
		op := makeLifecycleOperation(t, env, "applying", false, false)
		if _, err := env.e.Request(context.Background(), op.ID, "apply", ID()); !errors.Is(err, ErrBusy) {
			t.Fatalf("apply in applying state: %v", err)
		}
	})

	t.Run("retry requires failed or interrupted", func(t *testing.T) {
		env := newLifecycleEnv(t)
		op := makeLifecycleOperation(t, env, "prepared", false, false)
		if _, err := env.e.Request(context.Background(), op.ID, "retry", ID()); !errors.Is(err, ErrBusy) {
			t.Fatalf("retry from prepared: %v", err)
		}
	})

	t.Run("rollback requires snapshot", func(t *testing.T) {
		env := newLifecycleEnv(t)
		op := makeLifecycleOperation(t, env, "failed", false, false)
		if _, err := env.e.Request(context.Background(), op.ID, "rollback", ID()); err == nil ||
			!strings.Contains(err.Error(), "snapshot_unavailable") {
			t.Fatalf("rollback without snapshot accepted: %v", err)
		}
	})
}

func TestRetryPreservesOriginalSnapshotAfterPartialApply(t *testing.T) {
	env := newLifecycleEnv(t)
	seedLifecycleInstalled(t, env)
	op := makeLifecycleOperation(t, env, "prepared", false, false)

	// Force the first application to fail after several destinations, leaving a
	// genuinely partial install behind.
	outside := t.TempDir()
	if err := os.RemoveAll(env.fs.Path("/etc/sysusers.d")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, env.fs.Path("/etc/sysusers.d")); err != nil {
		t.Fatal(err)
	}
	attempt := launchLifecycleAttempt(t, env.e, op, "apply")
	if err := env.e.Run(context.Background(), op.ID, attempt); err == nil {
		t.Fatal("partial apply unexpectedly succeeded")
	}
	failed, err := env.e.Store.LoadID(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != "failed" || !failed.SnapshotReady {
		t.Fatalf("partial apply journal: state=%q snapshot=%v", failed.State, failed.SnapshotReady)
	}
	if readLifecycleFile(t, env.fs.Path("/usr/local/bin/xray-core-helper")) != "new-helper\n" {
		t.Fatal("partial apply did not mutate an earlier destination")
	}
	snapshot := filepath.Join(env.e.Store.Dir(op.ID), "snapshot")
	beforeDigest, err := digest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	oldHelper := snapshotEntryPath(t, failed.Before, "/usr/local/bin/xray-core-helper")
	if readLifecycleFile(t, filepath.Join(snapshot, oldHelper)) != "old-helper\n" {
		t.Fatal("snapshot does not contain the original helper")
	}

	if err := os.Remove(env.fs.Path("/etc/sysusers.d")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(env.fs.Path("/etc/sysusers.d"), 0755); err != nil {
		t.Fatal(err)
	}
	retryID := ID()
	retried, err := env.e.Request(context.Background(), op.ID, "retry", retryID)
	if err != nil {
		t.Fatalf("retry request: %v", err)
	}
	if len(retried.Attempts) != 2 || retried.Attempts[1].ID != retryID {
		t.Fatalf("retry attempt not journaled: %+v", retried.Attempts)
	}
	if err := env.e.Run(context.Background(), op.ID, retryID); err != nil {
		t.Fatalf("retry run: %v", err)
	}
	done, err := env.e.Store.LoadID(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != "succeeded" || done.Attempts[1].State != "succeeded" {
		t.Fatalf("retry did not finish cleanly: %+v", done)
	}
	afterDigest, err := digest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if beforeDigest != afterDigest {
		t.Fatal("retry replaced the original rollback snapshot with partially applied state")
	}
	if readLifecycleFile(t, filepath.Join(snapshot, oldHelper)) != "old-helper\n" {
		t.Fatal("retry overwrote the original snapshot content")
	}
}

func TestRunCorruptCandidateRejectedBeforeMutation(t *testing.T) {
	env := newLifecycleEnv(t)
	seedLifecycleInstalled(t, env)
	op := makeLifecycleOperation(t, env, "prepared", false, false)
	candidate := filepath.Join(env.e.Store.Dir(op.ID), "bundle/xray-core-helper/bin/xray-core-helper")
	writeLifecycleFile(t, candidate, "tampered\n")

	attempt := launchLifecycleAttempt(t, env.e, op, "apply")
	err := env.e.Run(context.Background(), op.ID, attempt)
	if err == nil || !strings.Contains(err.Error(), "candidate_changed") {
		t.Fatalf("corrupt candidate accepted: %v", err)
	}
	failed, loadErr := env.e.Store.LoadID(op.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if failed.State != "failed" || failed.SnapshotReady {
		t.Fatalf("corrupt candidate journal: state=%q snapshot=%v", failed.State, failed.SnapshotReady)
	}
	if readLifecycleFile(t, env.fs.Path("/usr/local/bin/xray-core-helper")) != "old-helper\n" {
		t.Fatal("corrupt candidate mutated an installed destination")
	}
	if len(env.runner.calls) != 1 || !strings.Contains(env.runner.calls[0], "systemctl stop xray-core-helper.service") {
		t.Fatalf("unexpected work before candidate check: %v", env.runner.calls)
	}
}

func TestRunVerifyFailureThenRollback(t *testing.T) {
	env := newLifecycleEnv(t)
	seedLifecycleInstalled(t, env)
	op := makeLifecycleOperation(t, env, "prepared", false, false)
	checks := 0
	env.e.Check = func(_ context.Context, op *Operation) error {
		checks++
		if op.TargetVersion != "helper-v2.0.0" {
			t.Fatalf("verification got target %q", op.TargetVersion)
		}
		if checks == 1 {
			return errors.New("new_version_unavailable: synthetic verification failure")
		}
		return nil
	}

	attempt := launchLifecycleAttempt(t, env.e, op, "apply")
	if err := env.e.Run(context.Background(), op.ID, attempt); err == nil {
		t.Fatal("verification failure was not returned")
	}
	failed, err := env.e.Store.LoadID(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != "failed" || failed.Error != "new_version_unavailable" {
		t.Fatalf("verification failure journal: state=%q error=%q", failed.State, failed.Error)
	}
	if readLifecycleFile(t, env.fs.Path("/usr/local/bin/xray-core-helper")) != "new-helper\n" {
		t.Fatal("new version was not applied before verification")
	}

	rollbackAttempt := launchLifecycleAttempt(t, env.e, failed, "rollback")
	if err := env.e.Run(context.Background(), op.ID, rollbackAttempt); err != nil {
		t.Fatalf("rollback run: %v", err)
	}
	rolledBack, err := env.e.Store.LoadID(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.State != "rolled_back" || checks != 2 {
		t.Fatalf("rollback journal: state=%q checks=%d", rolledBack.State, checks)
	}
	if readLifecycleFile(t, env.fs.Path("/usr/local/bin/xray-core-helper")) != "old-helper\n" {
		t.Fatal("rollback did not restore the original helper")
	}
	if readLifecycleFile(t, env.fs.Path("/usr/share/xray-core-helper/templConf/old.toml")) != "old-template\n" {
		t.Fatal("rollback did not restore the original template tree")
	}
	if _, err := os.Stat(env.fs.Path("/usr/share/xray-core-helper/templConf/new.toml")); !os.IsNotExist(err) {
		t.Fatal("rollback retained a new template file")
	}
}

func TestStatusBootIdentityAndWorkerIdentity(t *testing.T) {
	t.Run("boot change interrupts without systemctl", func(t *testing.T) {
		env := newLifecycleEnv(t)
		op := &Operation{
			SchemaVersion: 1,
			ID:            ID(),
			TargetVersion: "helper-v2.0.0",
			State:         "launching",
			ConfigPath:    env.e.ConfigPath,
			Attempts: []Attempt{{
				ID:        ID(),
				Action:    "apply",
				State:     "launching",
				BootID:    "previous-boot",
				StartedAt: time.Now().UTC(),
			}},
		}
		if err := env.e.Store.Save(op); err != nil {
			t.Fatal(err)
		}
		got, err := env.e.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.State != "interrupted" || got.Error != "installation_interrupted" {
			t.Fatalf("boot change not detected: state=%q error=%q", got.State, got.Error)
		}
		if len(env.runner.calls) != 0 {
			t.Fatalf("boot change queried systemd: %v", env.runner.calls)
		}
	})

	t.Run("active worker keeps applying with matching identity", func(t *testing.T) {
		env := newLifecycleEnv(t)
		op := &Operation{
			SchemaVersion: 1,
			ID:            ID(),
			TargetVersion: "helper-v2.0.0",
			State:         "applying",
			ConfigPath:    env.e.ConfigPath,
			Attempts: []Attempt{{
				ID:           ID(),
				Action:       "apply",
				State:        "applying",
				BootID:       currentBootID(t),
				InvocationID: "invocation-1",
				StartedAt:    time.Now().UTC(),
			}},
		}
		if err := env.e.Store.Save(op); err != nil {
			t.Fatal(err)
		}
		env.runner.output = []byte("LoadState=loaded\nActiveState=active\nInvocationID=invocation-1\n")
		got, err := env.e.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.State != "applying" {
			t.Fatalf("active worker was marked %q", got.State)
		}
		if len(env.runner.calls) != 1 || !strings.Contains(env.runner.calls[0], "systemctl show") {
			t.Fatalf("unexpected worker inspection: %v", env.runner.calls)
		}
	})

	t.Run("invocation mismatch fails closed", func(t *testing.T) {
		env := newLifecycleEnv(t)
		op := &Operation{
			SchemaVersion: 1,
			ID:            ID(),
			TargetVersion: "helper-v2.0.0",
			State:         "applying",
			ConfigPath:    env.e.ConfigPath,
			Attempts: []Attempt{{
				ID:           ID(),
				Action:       "apply",
				State:        "applying",
				BootID:       currentBootID(t),
				InvocationID: "expected",
				StartedAt:    time.Now().UTC(),
			}},
		}
		if err := env.e.Store.Save(op); err != nil {
			t.Fatal(err)
		}
		env.runner.output = []byte("LoadState=loaded\nActiveState=active\nInvocationID=other\n")
		if _, err := env.e.Status(context.Background()); err == nil ||
			!strings.Contains(err.Error(), "worker_identity_conflict") {
			t.Fatalf("invocation conflict accepted: %v", err)
		}
	})
}

func TestRunStopsBeforeMutationAndStartsAfterRuntimeUnlock(t *testing.T) {
	env := newLifecycleEnv(t)
	seedLifecycleInstalled(t, env)
	op := makeLifecycleOperation(t, env, "prepared", false, false)
	runtimeLock := filepath.Join(env.e.Store.Root, "runtime.lock")

	env.runner.onCall = func(line string) {
		switch {
		case strings.HasPrefix(line, "/usr/bin/systemctl stop xray-core-helper.service"):
			release, err := Lock(runtimeLock)
			if err != nil {
				t.Errorf("runtime lock held before service stop: %v", err)
				return
			}
			release()
		case strings.HasPrefix(line, "/usr/bin/systemctl daemon-reload"):
			if _, err := Lock(runtimeLock); !errors.Is(err, ErrBusy) {
				t.Errorf("runtime lock not held during mutation: %v", err)
			}
			if got := readLifecycleFile(t, env.fs.Path("/usr/local/bin/xray-core-helper")); got != "new-helper\n" {
				t.Errorf("daemon reload happened before destination mutation: %q", got)
			}
		case strings.HasPrefix(line, "/usr/bin/systemctl start xray-core-helper.service"):
			release, err := Lock(runtimeLock)
			if err != nil {
				t.Errorf("runtime lock still held before service start: %v", err)
				return
			}
			release()
		}
	}
	attempt := launchLifecycleAttempt(t, env.e, op, "apply")
	if err := env.e.Run(context.Background(), op.ID, attempt); err != nil {
		t.Fatalf("run apply: %v", err)
	}

	stop, reload, start := -1, -1, -1
	for i, call := range env.runner.calls {
		fields := strings.Fields(call)
		if len(fields) == 0 {
			t.Fatalf("empty command journal entry")
		}
		if fields[0] != "/usr/bin/systemctl" {
			t.Fatalf("unexpected executable in lifecycle: %q", call)
		}
		if strings.Contains(call, "tproxy") || strings.Contains(call, "/usr/local/bin/xray ") || strings.HasSuffix(call, "/xray ") {
			t.Fatalf("Xray/tproxy command executed: %q", call)
		}
		switch {
		case strings.Contains(call, " stop xray-core-helper.service"):
			stop = i
		case strings.Contains(call, " daemon-reload"):
			reload = i
		case strings.Contains(call, " start xray-core-helper.service"):
			start = i
		}
	}
	if stop < 0 || reload < 0 || start < 0 || !(stop < reload && reload < start) {
		t.Fatalf("service/mutation ordering is not stop < reload < start: %v", env.runner.calls)
	}
}

func TestRunStopFailureAbortsBeforeMutation(t *testing.T) {
	env := newLifecycleEnv(t)
	seedLifecycleInstalled(t, env)
	op := makeLifecycleOperation(t, env, "prepared", false, false)
	env.runner.failWith = "systemctl stop xray-core-helper.service"

	attempt := launchLifecycleAttempt(t, env.e, op, "apply")
	if err := env.e.Run(context.Background(), op.ID, attempt); err == nil {
		t.Fatal("stop failure was ignored")
	}
	failed, err := env.e.Store.LoadID(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != "failed" || failed.SnapshotReady {
		t.Fatalf("stop failure journal: state=%q snapshot=%v", failed.State, failed.SnapshotReady)
	}
	if readLifecycleFile(t, env.fs.Path("/usr/local/bin/xray-core-helper")) != "old-helper\n" {
		t.Fatal("stop failure still mutated a destination")
	}
	for _, call := range env.runner.calls {
		if strings.Contains(call, "daemon-reload") || strings.Contains(call, " start ") {
			t.Fatalf("lifecycle advanced after stop failure: %v", env.runner.calls)
		}
	}
}

func TestWrapBlocksOrdinaryMutationsDuringRecovery(t *testing.T) {
	env := newLifecycleEnv(t)
	op := &Operation{
		SchemaVersion: 1,
		ID:            ID(),
		TargetVersion: "helper-v2.0.0",
		State:         "failed",
		ConfigPath:    env.e.ConfigPath,
	}
	if err := env.e.Store.Save(op); err != nil {
		t.Fatal(err)
	}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	wrapped := env.e.Wrap(next)

	request := httptest.NewRequest(http.MethodPost, "/v1/xray/config", strings.NewReader("{}"))
	response := httptest.NewRecorder()
	wrapped.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("ordinary mutation status = %d, want %d", response.Code, http.StatusConflict)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "update_pending" || called {
		t.Fatalf("recovery gate response: code=%q nextCalled=%v", body.Error.Code, called)
	}

	response = httptest.NewRecorder()
	wrapped.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if !called || response.Code != http.StatusNoContent {
		t.Fatalf("read access blocked during recovery: called=%v status=%d", called, response.Code)
	}

	op.State = "succeeded"
	if err := env.e.Store.Save(op); err != nil {
		t.Fatal(err)
	}
	called = false
	response = httptest.NewRecorder()
	wrapped.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/xray/config", strings.NewReader("{}")))
	if !called || response.Code != http.StatusNoContent {
		t.Fatalf("terminal operation blocked ordinary mutation: called=%v status=%d", called, response.Code)
	}
}

func TestWrapUpdateStatusRedactsJournalAndValidatesRequests(t *testing.T) {
	env := newLifecycleEnv(t)
	writeLifecycleFile(t, env.fs.Path(ManifestPath), `{"schemaVersion":1,"version":"helper-v1.0.0","commit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","os":"linux","arch":"amd64","binaries":{}}`)
	op := makeLifecycleOperation(t, env, "prepared", false, false)
	op.Before = []Entry{{Path: "/secret", Exists: true, Hash: "fingerprint"}}
	op.CandidateDigest = "candidate-digest"
	op.InstallerDigest = "installer-digest"
	if err := env.e.Store.Save(op); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	env.e.Wrap(http.NotFoundHandler()).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/update", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d", response.Code)
	}
	var status struct {
		CurrentVersion string    `json:"currentVersion"`
		CurrentCommit  string    `json:"currentCommit"`
		Operation      Operation `json:"operation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.CurrentVersion != "helper-v1.0.0" || status.Operation.ID != op.ID || status.Operation.State != "prepared" {
		t.Fatalf("unexpected status payload: %+v", status)
	}
	if status.Operation.Before != nil || status.Operation.CandidateDigest != "" ||
		status.Operation.InstallerDigest != "" || status.Operation.ConfigPath != "" {
		t.Fatalf("private journal fields exposed: %+v", status.Operation)
	}

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"method", http.MethodPut, "/v1/update", "", http.StatusMethodNotAllowed},
		{"unknown endpoint", http.MethodPost, "/v1/update/nope", `{}`, http.StatusNotFound},
		{"check arguments", http.MethodPost, "/v1/update/check", `{"operationId":"x"}`, http.StatusBadRequest},
		{"unknown field", http.MethodPost, "/v1/update/check", `{"bogus":1}`, http.StatusBadRequest},
		{"trailing content", http.MethodPost, "/v1/update/check", `{}{}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			env.e.Wrap(http.NotFoundHandler()).ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", response.Code, tc.want, response.Body.String())
			}
		})
	}
}

func TestDirectPartialApplyAndRollback(t *testing.T) {
	env := newLifecycleEnv(t)
	seedLifecycleInstalled(t, env)
	op := makeLifecycleOperation(t, env, "prepared", false, false)

	outside := t.TempDir()
	if err := os.RemoveAll(env.fs.Path("/etc/sysusers.d")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, env.fs.Path("/etc/sysusers.d")); err != nil {
		t.Fatal(err)
	}
	if err := env.e.Direct(context.Background(), op.ID, "apply"); err == nil {
		t.Fatal("partial direct apply unexpectedly succeeded")
	}
	failed, err := env.e.Store.LoadID(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != "failed" || !failed.SnapshotReady {
		t.Fatalf("direct partial apply journal: state=%q snapshot=%v", failed.State, failed.SnapshotReady)
	}
	if got := readLifecycleFile(t, env.fs.Path("/usr/share/xray-core-helper/templConf/new.toml")); got != "new-template\n" {
		t.Fatalf("direct apply did not reach a later destination before failing: %q", got)
	}

	if err := os.Remove(env.fs.Path("/etc/sysusers.d")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(env.fs.Path("/etc/sysusers.d"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := env.e.Direct(context.Background(), op.ID, "rollback"); err != nil {
		t.Fatalf("direct rollback: %v", err)
	}
	rolledBack, err := env.e.Store.LoadID(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.State != "rolled_back" || len(rolledBack.Attempts) != 2 {
		t.Fatalf("direct rollback journal: %+v", rolledBack)
	}
	if readLifecycleFile(t, env.fs.Path("/usr/local/bin/xray-core-helper")) != "old-helper\n" {
		t.Fatal("direct rollback did not restore the original helper")
	}
	if readLifecycleFile(t, env.fs.Path("/usr/share/xray-core-helper/templConf/old.toml")) != "old-template\n" {
		t.Fatal("direct rollback did not restore the original template tree")
	}
	if _, err := os.Stat(env.fs.Path("/usr/share/xray-core-helper/templConf/new.toml")); !os.IsNotExist(err) {
		t.Fatal("direct rollback retained a new template file")
	}
}

func snapshotEntryPath(t *testing.T, entries []Entry, path string) string {
	t.Helper()
	for i, entry := range entries {
		if entry.Path == path {
			return strconv.Itoa(i)
		}
	}
	t.Fatalf("snapshot entry %q not found", path)
	return ""
}

func TestNativeDropinSourceDoesNotReplaceInstalledHook(t *testing.T) {
	bundle := t.TempDir()
	for _, target := range Targets(bundle, false) {
		if target.Path == "/etc/systemd/system/xray.service.d/50-tproxy.conf" {
			t.Fatal("update targets installed hook")
		}
	}
	source := filepath.Join(bundle, "xray-core-helper/packaging/systemd/50-tproxy.conf")
	writeLifecycleFile(t, source, "hook")
	n := 0
	for _, target := range Targets(bundle, false) {
		if target.Source == source {
			n++
		}
	}
	if n != 1 {
		t.Fatal("drop-in factory source missing", n)
	}
}
