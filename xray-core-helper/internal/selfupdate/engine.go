package selfupdate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"home-router/xray-core-helper/internal/commandrunner"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/setup"
	"home-router/xray-core-helper/internal/unixhttp"
	"home-router/xray-core-helper/internal/updaterelease"
	"home-router/xray-core-helper/internal/xrayservice"
)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type CommandRunner struct{}

func (CommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	runner, err := commandrunner.NewWithOutputLimit(name, 2*time.Minute, 1<<20)
	if err != nil {
		return nil, err
	}
	out, err := runner.Run(ctx, args)
	if err != nil {
		return nil, errors.New("installer command failed: " + filepath.Base(name))
	}
	return out.Stdout(), nil
}

type Manifest struct {
	SchemaVersion  int               `json:"schemaVersion"`
	Version        string            `json:"version"`
	Commit         string            `json:"commit"`
	OS             string            `json:"os"`
	Arch           string            `json:"arch"`
	Binaries       map[string]string `json:"binaries"`
	Dependencies   json.RawMessage   `json:"dependencies"`
	MinimumVersion string            `json:"minimum_installable_version,omitempty"`
}
type Engine struct {
	Store           *Store
	Files           Files
	Runner          Runner
	Releases        *updaterelease.Client
	ConfigPath      string
	InstallerSource string
	// ResolveAssets is a read-only systemd discovery hook; nil uses the config
	// projection in filesystem-only tests.
	ResolveAssets func(context.Context) (string, error)
	Setup         func(context.Context) error
	// Check is injectable for hermetic service verification, never HTTP input.
	Check func(context.Context, *Operation) error
}

func New() *Engine {
	return &Engine{Store: NewStore(StateRoot), Files: Files{SystemRoot: "/"}, Runner: CommandRunner{}, Releases: updaterelease.NewClient(), ResolveAssets: func(ctx context.Context) (string, error) {
		installation, err := xrayservice.New().Resolve(ctx)
		if err != nil {
			return "", err
		}
		if err = installation.CheckTrusted(); err != nil {
			return "", err
		}
		return installation.AssetDir, nil
	}, ConfigPath: bootstrap.DefaultConfigPath, InstallerSource: "/usr/local/bin/xray-core-helper"}
}
func (e *Engine) command(ctx context.Context, name string, args ...string) error {
	_, err := e.Runner.Run(ctx, name, args...)
	return err
}
func (e *Engine) unit(ctx context.Context, action, unit string) error {
	return e.command(ctx, "/usr/bin/systemctl", action, unit)
}
func (e *Engine) manifest() (Manifest, error) {
	var m Manifest
	raw, err := os.ReadFile(e.Files.Path(ManifestPath))
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(raw, &m)
	return m, err
}

func (e *Engine) CheckVersion(ctx context.Context) (map[string]any, error) {
	m, err := e.manifest()
	if err != nil {
		return nil, err
	}
	r, err := e.Releases.Latest(ctx, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"currentVersion": m.Version, "currentCommit": m.Commit, "latestVersion": r.Version, "updateAvailable": false, "comparison": "unknown"}
	if n, ce := updaterelease.Compare(r.Version, m.Version); ce == nil {
		result["comparison"] = "known"
		result["updateAvailable"] = n > 0
	}
	return result, nil
}
func CheckDowngrade(current Manifest, target string) error {
	if err := updaterelease.ValidateVersion(target); err != nil {
		return err
	}
	if current.MinimumVersion != "" {
		n, err := updaterelease.Compare(target, current.MinimumVersion)
		if err != nil || n < 0 {
			return errors.New("downgrade_forbidden: target is below minimum_installable_version")
		}
	}
	return nil
}

func (e *Engine) Prepare(ctx context.Context, version string, web *bool) (*Operation, error) {
	return e.prepare(ctx, version, web, "")
}

// PrepareLocal is available only to the privileged standalone CLI, never HTTP.
func (e *Engine) PrepareLocal(ctx context.Context, root string, web *bool) (*Operation, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	b, err := updaterelease.VerifyLocal(absolute, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	return e.prepare(ctx, b.Version, web, absolute)
}
func (e *Engine) prepare(ctx context.Context, version string, web *bool, local string) (*Operation, error) {
	unlock, err := Lock(filepath.Join(e.Store.Root, "operation.lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	previous, err := e.Store.Load()
	if err != nil {
		return nil, err
	}
	reprepare := previous != nil && !previous.SnapshotReady && previous.CandidateDigest == "" && (previous.State == "failed" || previous.State == "interrupted")
	if previous != nil && !terminal(previous.State) && !reprepare {
		return nil, ErrBusy
	}
	if reprepare {
		if version != "" && version != previous.TargetVersion {
			return nil, ErrBusy
		}
		version = previous.TargetVersion
		v := previous.Web
		web = &v
	}
	m, err := e.manifest()
	if err != nil {
		return nil, err
	}
	if version == "" {
		r, err := e.Releases.Latest(ctx, runtime.GOARCH)
		if err != nil {
			return nil, err
		}
		version = r.Version
	}
	if err = CheckDowngrade(m, version); err != nil {
		return nil, err
	}
	var r updaterelease.Release
	if local == "" {
		r, err = e.Releases.Resolve(ctx, version, runtime.GOARCH)
		if err != nil {
			return nil, err
		}
	}
	_, stErr := os.Lstat(e.Files.Path("/usr/local/sbin/xray-core-helperd"))
	fresh := os.IsNotExist(stErr)
	if stErr != nil && !fresh {
		return nil, stErr
	}
	webEnabled := false
	if !fresh {
		raw, err := os.ReadFile(e.Files.Path("/etc/xray-core-helper/helper.env"))
		if err != nil {
			return nil, errors.New("installed_components_unknown: helper.env unavailable")
		}
		switch strings.TrimSpace(string(raw)) {
		case "XRAY_HELPER_WEB_ENABLED=1":
			webEnabled = true
		case "XRAY_HELPER_WEB_ENABLED=0":
		default:
			return nil, errors.New("installed_components_unknown: invalid helper.env")
		}
		if web != nil && *web != webEnabled {
			return nil, errors.New("component_change_forbidden: update preserves installed components")
		}
	} else if web != nil {
		webEnabled = *web
	} else {
		webEnabled = true
	}
	if !fresh {
		if err = e.Preflight(ctx); err != nil {
			return nil, err
		}
	}
	op := &Operation{SchemaVersion: 1, ID: ID(), SourceVersion: m.Version, TargetVersion: version, Arch: runtime.GOARCH, Fresh: fresh, Web: webEnabled, State: "preparing", Stage: "download", CreatedAt: time.Now().UTC(), ConfigPath: e.ConfigPath}
	if reprepare {
		op.ID = previous.ID
		op.CreatedAt = previous.CreatedAt
		op.Attempts = previous.Attempts
		if err = os.RemoveAll(filepath.Join(e.Store.Dir(op.ID), "bundle")); err != nil {
			return nil, err
		}
	}
	if err = e.Store.Save(op); err != nil {
		return nil, err
	}
	fail := func(cause error) (*Operation, error) {
		op.Fail(errorCode(cause))
		_ = e.Store.Save(op)
		return op, cause
	}
	bundleDir := filepath.Join(e.Store.Dir(op.ID), "bundle")
	var b updaterelease.Bundle
	if local == "" {
		b, err = updaterelease.Stage(ctx, e.Releases, r, bundleDir)
	} else {
		err = copyTree(local, bundleDir)
		if err == nil {
			b, err = updaterelease.VerifyLocal(bundleDir, runtime.GOARCH)
		}
	}
	if err != nil {
		return fail(err)
	}
	if b.Version != version || b.Arch != runtime.GOARCH {
		return fail(errors.New("release_identity_mismatch"))
	}
	op.Commit = b.Commit
	op.MinimumVersion = b.MinimumInstallableVersion
	if webEnabled {
		// npm rejects loading one path as both user and global configuration.
		// Keep distinct empty files so host npm settings remain excluded.
		userConfig := filepath.Join(e.Store.Dir(op.ID), "npm-user.conf")
		globalConfig := filepath.Join(e.Store.Dir(op.ID), "npm-global.conf")
		for _, path := range []string{userConfig, globalConfig} {
			if err = atomicWrite(path, nil, 0600); err != nil {
				return fail(err)
			}
		}
		if err = e.command(ctx, "/usr/bin/npm", "--prefix", filepath.Join(bundleDir, "xrayui-web"), "ci", "--omit=dev", "--ignore-scripts", "--no-audit", "--no-fund", "--cache", filepath.Join(e.Store.Root, "npm-cache"), "--userconfig", userConfig, "--globalconfig", globalConfig); err != nil {
			return fail(errors.New("web_dependencies_failed: npm preparation failed before service shutdown"))
		}
		// npm creates .bin links; they are unnecessary to run the web server.
		if err = os.RemoveAll(filepath.Join(bundleDir, "xrayui-web/node_modules/.bin")); err != nil {
			return fail(err)
		}
	}
	// Resolve Xray and validate installation settings before stopping services.
	configInput := e.Files.Path(e.ConfigPath)
	if _, err = os.Stat(configInput); os.IsNotExist(err) {
		configInput = filepath.Join(bundleDir, "xray-core-helper/config.example.toml")
	}
	paths, err := e.Runner.Run(ctx, e.InstallerSource, "-config", configInput, "discover-xray", "--systemd")
	if err != nil {
		return fail(errors.New("xray_installation_unavailable"))
	}
	pathsHash := sha256.Sum256(paths)
	op.InstallationPathsDigest = hex.EncodeToString(pathsHash[:])
	if err = atomicWrite(filepath.Join(e.Store.Dir(op.ID), "installation-paths.conf"), paths, 0600); err != nil {
		return fail(err)
	}
	for _, t := range Targets(bundleDir, webEnabled) {
		if _, err = os.Stat(t.Source); err != nil {
			return fail(errors.New("release_incomplete: required bundle file missing"))
		}
	}
	op.CandidateDigest, err = digest(bundleDir)
	if err != nil {
		return fail(err)
	}
	// Keep the already verified installer outside the directories it replaces.
	installer := filepath.Join(e.Store.Root, "installer")
	content, err := os.ReadFile(e.InstallerSource)
	if err != nil {
		return fail(err)
	}
	h := sha256.Sum256(content)
	op.InstallerDigest = hex.EncodeToString(h[:])
	if err = atomicWrite(installer, content, 0700); err != nil {
		return fail(err)
	}
	if err = atomicWrite(installer+".sha256", []byte(op.InstallerDigest), 0600); err != nil {
		return fail(err)
	}
	before, err := e.Files.Inspect(pathsFor(Targets(bundleDir, webEnabled), "/etc/systemd/system/xray-core-helper.service.d/20-installation-paths.conf"))
	if err != nil {
		return fail(err)
	}
	op.Before = before
	if err = e.CheckSpace(op, false); err != nil {
		return fail(err)
	}
	op.State = "prepared"
	op.Set("prepared")
	if reprepare {
		op.Set("reprepared")
	}
	if err = e.Store.Save(op); err != nil {
		return nil, err
	}
	return op, nil
}

func (e *Engine) Preflight(ctx context.Context) error {
	cfg, err := bootstrap.Load(e.Files.Path(e.ConfigPath))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("settings_invalid")
	}
	for _, dataPath := range []string{cfg.Storage.Database, e.ConfigPath} {
		physical := e.Files.Path(dataPath)
		if physical == e.storeRoot() || strings.HasPrefix(physical, e.storeRoot()+string(os.PathSeparator)) {
			return errors.New("settings_invalid: helper data must remain outside the update journal")
		}
	}
	if e.ResolveAssets != nil {
		cfg.Xray.AssetDir, err = e.ResolveAssets(ctx)
		if err != nil {
			return errors.New("xray_installation_unavailable")
		}
	}
	for _, pending := range []string{setup.StateRoot + "/pending.json", filepath.Join(cfg.Xray.AssetDir, ".xray-core-helper-pending"), filepath.Join(cfg.Xray.AssetDir, ".xray-core-helper-consolidation-pending")} {
		if _, statErr := os.Lstat(e.Files.Path(pending)); statErr == nil || !os.IsNotExist(statErr) {
			return errors.New("recovery_pending: unfinished configuration or geodata operation")
		}
	}
	if err = setup.CheckPendingDatabase(ctx, e.Files.Path(cfg.Storage.Database)); err != nil {
		return errors.New("recovery_pending: finish configuration or safe-apply recovery first")
	}
	path := e.Files.Path(cfg.Storage.Database)
	if _, err = os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	for _, table := range []struct{ name, condition string }{{"proxy_domain_control", "consolidation_active=1"}, {"geodata_update_operation", "state IN ('running','recovery_required')"}} {
		var exists, count int
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table.name).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			if err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+table.name+" WHERE "+table.condition).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return errors.New("recovery_pending: maintenance operation in progress")
			}
		}
	}
	return nil
}

func unitName(attempt string) string { return "xray-core-helper-update-" + attempt + ".service" }
func (e *Engine) Request(ctx context.Context, id, action, actionID string) (*Operation, error) {
	if err := requiredID(id); err != nil {
		return nil, err
	}
	if err := requiredID(actionID); err != nil {
		return nil, err
	}
	if action != "apply" && action != "retry" && action != "rollback" {
		return nil, errors.New("invalid update action")
	}
	if action == "retry" {
		old, err := e.Store.LoadID(id)
		if err != nil {
			return nil, err
		}
		if !old.SnapshotReady && old.CandidateDigest == "" && (old.State == "failed" || old.State == "interrupted") {
			if _, err = e.Prepare(ctx, old.TargetVersion, &old.Web); err != nil {
				return nil, err
			}
		}
	}
	unlock, err := Lock(filepath.Join(e.Store.Root, "operation.lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	op, err := e.Store.LoadID(id)
	if err != nil {
		return nil, err
	}
	for _, a := range op.Attempts {
		if a.ID == actionID {
			if a.Action != action {
				return nil, errors.New("action_id_conflict")
			}
			return op, nil
		}
	}
	if action == "apply" && op.State != "prepared" {
		return nil, ErrBusy
	}
	if action != "apply" && op.State != "failed" && op.State != "interrupted" && !(action == "retry" && op.State == "prepared" && op.Stage == "reprepared") {
		return nil, ErrBusy
	}
	if action == "rollback" && !op.SnapshotReady {
		return nil, errors.New("snapshot_unavailable")
	}
	if action != "rollback" {
		if err = e.Preflight(ctx); err != nil {
			return nil, err
		}
	}
	if err = e.verifyInstaller(op); err != nil {
		return nil, err
	}
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	op.Attempts = append(op.Attempts, Attempt{ID: actionID, Action: action, State: "launching", Stage: "launching", BootID: strings.TrimSpace(string(boot)), StartedAt: time.Now().UTC()})
	op.State = "launching"
	op.Error = ""
	op.Set("launching")
	if err = e.Store.Save(op); err != nil {
		return nil, err
	}
	err = e.command(ctx, "/usr/bin/systemd-run", "--unit="+unitName(actionID), "--collect", "--no-block", "--property=Type=exec", "--property=TimeoutStopSec=30", "--property=UMask=0077", filepath.Join(e.Store.Root, "installer"), "install", "--worker", id, "--attempt", actionID)
	if err != nil {
		op.Fail("worker_launch_failed")
		_ = e.Store.Save(op)
		return op, err
	}
	return op, nil
}

// Direct is the bootstrap/terminal entry point. It does not require a running
// service or systemd transient unit, and retains the same operation journal.
func (e *Engine) Direct(ctx context.Context, id, action string) error {
	saved, err := e.Store.LoadID(id)
	if err != nil {
		return err
	}
	if saved.ConfigPath != "" {
		e.ConfigPath = saved.ConfigPath
	}
	if action == "retry" {
		old, err := e.Store.LoadID(id)
		if err != nil {
			return err
		}
		if !old.SnapshotReady && old.CandidateDigest == "" && (old.State == "failed" || old.State == "interrupted") {
			if _, err = e.Prepare(ctx, old.TargetVersion, &old.Web); err != nil {
				return err
			}
			action = "apply"
		}
	}
	unlock, err := Lock(filepath.Join(e.Store.Root, "operation.lock"))
	if err != nil {
		return err
	}
	op, err := e.Store.LoadID(id)
	if err != nil {
		unlock()
		return err
	}
	if (action == "apply" && op.State != "prepared") || (action != "apply" && op.State != "failed" && op.State != "interrupted") || (action != "apply" && action != "retry" && action != "rollback") {
		unlock()
		return ErrBusy
	}
	if action == "rollback" && !op.SnapshotReady {
		unlock()
		return errors.New("snapshot_unavailable")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		unlock()
		return err
	}
	idAttempt := ID()
	op.Attempts = append(op.Attempts, Attempt{ID: idAttempt, Action: action, State: "launching", Stage: "launching", BootID: strings.TrimSpace(string(boot)), StartedAt: time.Now().UTC()})
	op.State = "launching"
	op.Set("launching")
	err = e.Store.Save(op)
	unlock()
	if err != nil {
		return err
	}
	return e.Run(ctx, id, idAttempt)
}
func (e *Engine) verifyInstaller(op *Operation) error {
	f, err := os.OpenFile(filepath.Join(e.Store.Root, "installer"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("installer_unavailable")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return errors.New("installer_untrusted")
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || st.Size() > 256<<20 || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
		return errors.New("installer_untrusted")
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != op.InstallerDigest {
		return errors.New("installer_checksum_mismatch")
	}
	return nil
}

func (e *Engine) Status(ctx context.Context) (*Operation, error) {
	op, err := e.Store.Load()
	if err != nil || op == nil {
		return op, err
	}
	if op.State != "launching" && op.State != "applying" && op.State != "verifying" && op.State != "preparing" {
		return op, nil
	}
	unlock, err := Lock(filepath.Join(e.Store.Root, "operation.lock"))
	if errors.Is(err, ErrBusy) {
		return op, nil
	}
	if err != nil {
		return nil, err
	}
	defer unlock()
	op, err = e.Store.Load()
	if err != nil || op == nil {
		return op, err
	}
	if op.State != "launching" && op.State != "applying" && op.State != "verifying" && op.State != "preparing" {
		return op, nil
	}
	if op.State == "preparing" {
		op.State = "interrupted"
		op.Error = "preparation_interrupted"
		if err = e.Store.Save(op); err != nil {
			return nil, err
		}
		return op, nil
	}
	if len(op.Attempts) == 0 {
		return nil, errors.New("invalid update attempts")
	}
	a := &op.Attempts[len(op.Attempts)-1]
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	interrupted := a.BootID != strings.TrimSpace(string(boot))
	if !interrupted {
		out, err := e.Runner.Run(ctx, "/usr/bin/systemctl", "show", unitName(a.ID), "--property=LoadState,ActiveState,InvocationID", "--no-pager")
		if err != nil {
			return nil, errors.New("worker_state_unknown")
		}
		props := properties(out)
		if props["LoadState"] == "" || props["ActiveState"] == "" {
			return nil, errors.New("worker_state_unknown")
		}
		if props["ActiveState"] == "active" || props["ActiveState"] == "activating" {
			if a.InvocationID != "" && props["InvocationID"] != a.InvocationID {
				return nil, errors.New("worker_identity_conflict")
			}
			return op, nil
		}
		if (op.State == "launching" || a.InvocationID == "") && time.Since(a.StartedAt) < 30*time.Second {
			return op, nil
		}
		interrupted = true
	}
	if interrupted {
		op.State = "interrupted"
		op.Error = "installation_interrupted"
		a.State = "interrupted"
		if err = e.Store.Save(op); err != nil {
			return nil, err
		}
	}
	return op, nil
}
func properties(b []byte) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}

func (e *Engine) Run(ctx context.Context, id, attemptID string) (result error) {
	unlock, err := waitForWorkerLock(ctx, filepath.Join(e.Store.Root, "operation.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	op, err := e.Store.LoadID(id)
	if err != nil {
		return err
	}
	if len(op.Attempts) == 0 {
		return errors.New("attempt missing")
	}
	if op.ConfigPath != "" {
		e.ConfigPath = op.ConfigPath
	}
	a := &op.Attempts[len(op.Attempts)-1]
	if a.ID != attemptID || a.State != "launching" {
		return errors.New("stale_attempt")
	}
	a.InvocationID = os.Getenv("INVOCATION_ID")
	op.State = "applying"
	a.State = "applying"
	op.Set("stop_services")
	if err = e.Store.Save(op); err != nil {
		return err
	}
	defer func() {
		if result != nil {
			op.Fail(errorCode(result))
			_ = e.Store.Save(op)
		}
	}()
	if err = e.CheckSpace(op, a.Action == "rollback"); err != nil {
		return err
	}
	if err = e.unit(ctx, "stop", "xray-core-helper.service"); err != nil && !op.Fresh {
		return err
	}
	if op.Web && !op.Fresh {
		if err = e.unit(ctx, "stop", "xrayui-web.service"); err != nil {
			return err
		}
	}
	releaseRuntime, err := Lock(filepath.Join(e.Store.Root, "runtime.lock"))
	if err != nil {
		return err
	}
	defer func() {
		if releaseRuntime != nil {
			releaseRuntime()
		}
	}()
	if a.Action != "rollback" {
		if err = e.Preflight(ctx); err != nil {
			return err
		}
		bundle := filepath.Join(e.Store.Dir(op.ID), "bundle")
		hash, err := digest(bundle)
		if err != nil || hash != op.CandidateDigest {
			return errors.New("candidate_changed")
		}
		if !op.SnapshotReady {
			if err = e.Files.Unchanged(op.Before); err != nil {
				return err
			}
			dataPaths := []string{"/etc/xray-core-helper", "/var/lib/xray-core-helper", e.ConfigPath}
			if cfg, err := bootstrap.Load(e.Files.Path(e.ConfigPath)); err == nil {
				dataPaths = append(dataPaths, cfg.Storage.Database, cfg.Storage.Database+"-wal", cfg.Storage.Database+"-shm")
			}
			all := pathsFor(Targets(bundle, op.Web), append(dataPaths, "/etc/systemd/system/xray-core-helper.service.d/20-installation-paths.conf")...)
			op.Before, err = e.Files.Inspect(all)
			if err != nil {
				return err
			}
			op.Set("snapshot")
			if err = e.Store.Save(op); err != nil {
				return err
			}
			if err = e.Files.Snapshot(op, filepath.Join(e.Store.Dir(id), "snapshot"), func() error { return e.Store.Save(op) }); err != nil {
				return err
			}
		}
		if err = e.Files.Apply(op, Targets(bundle, op.Web), func() error { return e.Store.Save(op) }); err != nil {
			return err
		}
		paths, err := os.ReadFile(filepath.Join(e.Store.Dir(op.ID), "installation-paths.conf"))
		if err != nil {
			return err
		}
		ph := sha256.Sum256(paths)
		if hex.EncodeToString(ph[:]) != op.InstallationPathsDigest {
			return errors.New("installation_paths_changed")
		}
		op.Set("installation_paths")
		if err = e.Store.Save(op); err != nil {
			return err
		}
		if err = atomicWrite(e.Files.Path("/etc/systemd/system/xray-core-helper.service.d/20-installation-paths.conf"), paths, 0644); err != nil {
			return err
		}
		if op.Fresh {
			if err = e.initialize(ctx, op, bundle); err != nil {
				return err
			}
		}
	} else {
		if op.Fresh {
			if err = e.unit(ctx, "disable", "xray-core-helper.service"); err != nil {
				return err
			}
		}
		if err = e.Files.Restore(op, filepath.Join(e.Store.Dir(id), "snapshot"), func() error { return e.Store.Save(op) }); err != nil {
			return err
		}
		op.Set("rollback_cleanup")
		if err = e.Store.Save(op); err != nil {
			return err
		}
		if err = e.Files.CleanupCandidate(Targets(filepath.Join(e.Store.Dir(op.ID), "bundle"), op.Web)); err != nil {
			return err
		}

	}
	op.Set("daemon_reload")
	if err = e.Store.Save(op); err != nil {
		return err
	}
	if err = e.command(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return err
	}
	if op.Fresh && a.Action != "rollback" && e.Setup != nil {
		op.Set("setup")
		if err = e.Store.Save(op); err != nil {
			return err
		}
		if err = e.Setup(ctx); err != nil {
			return err
		}
	}
	// Release exclusive writing ownership before the new daemon acquires it.
	releaseRuntime()
	releaseRuntime = nil
	op.State = "verifying"
	a.State = "verifying"
	op.Set("verify")
	if err = e.Store.Save(op); err != nil {
		return err
	}
	if op.Fresh && a.Action == "rollback" {
		op.Finish("rolled_back")
		return e.Store.Save(op)
	}
	if err = e.unit(ctx, "start", "xray-core-helper.service"); err != nil {
		return err
	}
	if op.Web {
		// Type=simple reports the daemon started before it has created web.env.
		// The HTTP listener is opened after web runtime preparation completes.
		if e.Check == nil {
			if err = e.verifyServices(ctx, op, false); err != nil {
				return err
			}
		}
		if err = e.unit(ctx, "start", "xrayui-web.service"); err != nil {
			return err
		}
	}
	if e.Check != nil {
		err = e.Check(ctx, op)
	} else {
		err = e.verify(ctx, op)
	}
	if err != nil {
		return err
	}
	state := "succeeded"
	if a.Action == "rollback" {
		state = "rolled_back"
	}
	op.Finish(state)
	return e.Store.Save(op)
}

func (e *Engine) initialize(ctx context.Context, op *Operation, bundle string) error {
	var err error
	h := filepath.Join(bundle, "xray-core-helper")
	for _, dir := range []string{"/usr/local/libexec/xray-tproxy", "/run/xray-tproxy"} {
		if err = os.MkdirAll(e.Files.Path(dir), 0755); err != nil {
			return err
		}
	}
	if err = e.command(ctx, "/usr/bin/systemd-sysusers", filepath.Join(h, "packaging/sysusers.d/xray-core-helper.conf")); err != nil {
		return err
	}
	config := e.Files.Path(op.ConfigPath)
	if _, err = os.Stat(config); os.IsNotExist(err) {
		raw, err := os.ReadFile(filepath.Join(h, "config.example.toml"))
		if err != nil {
			return err
		}
		if err = atomicWrite(config, raw, 0640); err != nil {
			return err
		}
	}
	env := "XRAY_HELPER_WEB_ENABLED=0\n"
	if op.Web {
		env = "XRAY_HELPER_WEB_ENABLED=1\n"
		if err = e.command(ctx, "/usr/bin/systemd-sysusers", filepath.Join(bundle, "xrayui-web/packaging/sysusers.d/xrayui-web.conf")); err != nil {
			return err
		}
	}
	if err = atomicWrite(e.Files.Path("/etc/xray-core-helper/helper.env"), []byte(env), 0600); err != nil {
		return err
	}
	if err = e.command(ctx, "/usr/bin/systemd-tmpfiles", "--create", filepath.Join(h, "packaging/tmpfiles.d/xray-core-helper.conf")); err != nil {
		return err
	}
	return e.unit(ctx, "enable", "xray-core-helper.service")
}
func (e *Engine) verify(ctx context.Context, op *Operation) error {
	return e.verifyServices(ctx, op, op.Web)
}

func (e *Engine) verifyServices(ctx context.Context, op *Operation, checkWeb bool) error {
	cfg, err := bootstrap.Load(e.Files.Path(e.ConfigPath))
	if err != nil {
		return errors.New("verify_settings_failed")
	}
	client := unixhttp.NewClient(cfg.Server.Socket, 2*time.Second)
	expected := op.TargetVersion + "+" + op.Commit[:12]
	if op.Attempts[len(op.Attempts)-1].Action == "rollback" {
		m, err := e.manifest()
		if err != nil {
			return err
		}
		expected = m.Version
		if len(m.Commit) >= 12 {
			expected += "+" + m.Commit[:12]
		}
	}
	wait, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for {
		req, _ := http.NewRequestWithContext(wait, "GET", "http://helper/v1/health", nil)
		resp, err := client.Do(req)
		ok := false
		if err == nil {
			var health struct {
				Version string `json:"version"`
				Status  string `json:"status"`
			}
			de := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&health)
			resp.Body.Close()
			ok = resp.StatusCode == 200 && de == nil && (health.Status == "ok" || health.Status == "setup_required") && (expected == "" || health.Version == expected)
		}
		if ok && checkWeb {
			host := cfg.Web.Host
			if host == "0.0.0.0" || host == "::" {
				host = "127.0.0.1"
			}
			req, _ = http.NewRequestWithContext(wait, "GET", "http://"+net.JoinHostPort(host, strconv.Itoa(cfg.Web.Port))+"/", nil)
			plain := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err = plain.Do(req)
			ok = err == nil && resp.StatusCode == 200
			if resp != nil {
				resp.Body.Close()
			}
		}
		if ok {
			return nil
		}
		select {
		case <-wait.Done():
			return errors.New("new_version_unavailable: helper or web verification failed")
		case <-time.After(500 * time.Millisecond):
		}
	}
}
func errorCode(err error) string {
	for _, item := range []struct {
		cause error
		code  string
	}{
		{updaterelease.ErrChecksumMismatch, "release_checksum_mismatch"},
		{updaterelease.ErrInvalidManifest, "release_manifest_invalid"},
		{updaterelease.ErrUnsafeArchive, "release_archive_unsafe"},
		{updaterelease.ErrInvalidArchive, "release_archive_invalid"},
		{updaterelease.ErrInvalidChecksums, "release_checksums_invalid"},
		{updaterelease.ErrReleaseNotFound, "release_not_found"},
		{updaterelease.ErrInvalidRelease, "release_incomplete"},
		{updaterelease.ErrURLRejected, "release_origin_rejected"},
		{updaterelease.ErrResponseTooLarge, "release_too_large"},
		{updaterelease.ErrDownload, "release_download_failed"},
		{updaterelease.ErrAPI, "release_api_unavailable"},
	} {
		if errors.Is(err, item.cause) {
			return item.code
		}
	}
	code, _, _ := strings.Cut(err.Error(), ":")
	if len(code) > 80 || strings.ContainsAny(code, " /\\\n") {
		return "installation_failed"
	}
	return code
}

// The transient unit may begin before systemd-run returns to Request. Wait only
// for this short launch handoff; ordinary callers still use a nonblocking lock.
func waitForWorkerLock(ctx context.Context, path string) (func(), error) {
	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		release, err := Lock(path)
		if !errors.Is(err, ErrBusy) {
			return release, err
		}
		select {
		case <-wait.Done():
			return nil, wait.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (e *Engine) storeRoot() string {
	if e.Store == nil {
		return e.Files.Path(StateRoot)
	}
	return filepath.Clean(e.Store.Root)
}
