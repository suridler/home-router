package selfupdate

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/xrayservice"
)

// XrayUpgrade is a separate journal: helper releases never replace Xray, and
// Xray recovery never restores helper settings or geodata.
type XrayUpgrade struct {
	Events          []string  `json:"events,omitempty"`
	ID              string    `json:"id"`
	ActionID        string    `json:"actionId"`
	Action          string    `json:"action"`
	Version         string    `json:"version"`
	PreviousVersion string    `json:"previousVersion"`
	State           string    `json:"state"`
	Stage           string    `json:"stage"`
	Error           string    `json:"error,omitempty"`
	BackupReady     bool      `json:"backupReady"`
	Changed         bool      `json:"changed"`
	UpdatedAt       time.Time `json:"updatedAt"`
	Binary          string    `json:"binary,omitempty"`
	Config          string    `json:"config,omitempty"`
	Assets          string    `json:"assets,omitempty"`
	CandidateHash   string    `json:"candidateHash,omitempty"`
	BackupHash      string    `json:"backupHash,omitempty"`
	ConfigHash      string    `json:"configHash,omitempty"`
}

func (s *XrayUpgrade) Public() any {
	if s == nil {
		return nil
	}
	return map[string]any{"id": s.ID, "actionId": s.ActionID, "action": s.Action, "version": s.Version, "previousVersion": s.PreviousVersion, "state": s.State, "stage": s.Stage, "error": s.Error, "backupReady": s.BackupReady, "changed": s.Changed, "updatedAt": s.UpdatedAt, "events": s.Events}
}
func (e *Engine) xrayUpgradeState() (*XrayUpgrade, error) {
	var s XrayUpgrade
	err := readJSON(filepath.Join(e.Store.Root, "xray-upgrade.json"), &s)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !validID.MatchString(s.ID) || !validID.MatchString(s.ActionID) || !xrayStableVersion.MatchString(s.Version) {
		return nil, errors.New("xray_upgrade_journal_invalid")
	}
	return &s, nil
}
func (e *Engine) saveXrayUpgrade(s *XrayUpgrade) error {
	s.UpdatedAt = time.Now().UTC()
	return writeJSON(filepath.Join(e.Store.Root, "xray-upgrade.json"), s)
}
func (e *Engine) xrayUpgradeDir(s *XrayUpgrade) string {
	return filepath.Join(e.Store.Root, "xray-upgrades", s.ID)
}
func xrayUpgradeActive(s *XrayUpgrade) bool {
	return s != nil && (s.State == "launching" || s.State == "running")
}
func (e *Engine) XrayUpgradeStatus(ctx context.Context) (*XrayUpgrade, error) {
	s, err := e.xrayUpgradeState()
	if err != nil || !xrayUpgradeActive(s) || time.Since(s.UpdatedAt) < 10*time.Second {
		return s, err
	}
	// A worker cannot survive a reboot. Failed/absent transient units leave an
	// explicit recovery choice rather than silently repeating an installation.
	raw, err := e.Runner.Run(ctx, "/usr/bin/systemctl", "show", xrayUpgradeUnit(s.ActionID), "--property=ActiveState", "--value", "--no-pager")
	if err != nil {
		return nil, err
	}
	switch strings.TrimSpace(string(raw)) {
	case "active", "activating", "deactivating":
		return s, nil
	}
	unlock, err := Lock(filepath.Join(e.Store.Root, "operation.lock"))
	if err != nil {
		return s, nil
	}
	defer unlock()
	current, err := e.xrayUpgradeState()
	if err != nil {
		return nil, err
	}
	if current != nil && current.ActionID == s.ActionID && xrayUpgradeActive(current) {
		current.State = "failed"
		current.Error = "xray_upgrade_interrupted"
		if err = e.saveXrayUpgrade(current); err != nil {
			return nil, err
		}
		s = current
	}
	return s, nil
}
func xrayUpgradeUnit(id string) string { return "xray-core-helper-xray-upgrade-" + id + ".service" }
func latestXrayVersion(ctx context.Context, client *http.Client) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	data, err := xrayDownload(ctx, client, xrayLatestURL)
	if err != nil {
		return "", errors.New("xray_release_unavailable")
	}
	var r struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if json.Unmarshal(data, &r) != nil || r.Draft || r.Prerelease || !xrayStableVersion.MatchString(r.Tag) {
		return "", errors.New("xray_release_invalid")
	}
	return r.Tag, nil
}
func (e *Engine) resolveUpgradeXray(ctx context.Context) (xrayservice.Installation, error) {
	if e.XrayInstallation != nil {
		return e.XrayInstallation(ctx)
	}
	return xrayservice.New().Resolve(ctx)
}
func (e *Engine) currentXray(ctx context.Context) (xrayservice.Installation, string, error) {
	i, err := e.resolveUpgradeXray(ctx)
	if err != nil {
		return i, "", err
	}
	version, err := e.Runner.Run(ctx, i.Binary, "version")
	if err != nil {
		return i, "", err
	}
	fields := strings.Fields(string(version))
	if len(fields) < 2 || fields[0] != "Xray" {
		return i, "", errors.New("xray_version_unavailable")
	}
	return i, fields[1], nil
}
func (e *Engine) CheckXrayUpgrade(ctx context.Context) (any, error) {
	_, current, err := e.currentXray(ctx)
	if err != nil {
		return nil, err
	}
	latest, err := latestXrayVersion(ctx, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"currentVersion": current, "latestVersion": latest, "updateAvailable": xrayVersionNewer(latest, current)}, nil
}
func xrayVersionNewer(latest, current string) bool {
	var a, b [3]int
	if _, err := fmt.Sscanf(strings.TrimPrefix(latest, "v"), "%d.%d.%d", &a[0], &a[1], &a[2]); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(strings.TrimPrefix(current, "v"), "%d.%d.%d", &b[0], &b[1], &b[2]); err != nil {
		return false
	}
	for n := range a {
		if a[n] != b[n] {
			return a[n] > b[n]
		}
	}
	return false
}
func (e *Engine) RequestXrayUpgrade(ctx context.Context, action string, input Request) (*XrayUpgrade, error) {
	if requiredID(input.ActionID) != nil {
		return nil, errors.New("invalid action id")
	}
	if _, err := e.XrayUpgradeStatus(ctx); err != nil {
		return nil, err
	}
	unlock, err := Lock(filepath.Join(e.Store.Root, "operation.lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	old, err := e.xrayUpgradeState()
	if err != nil {
		return nil, err
	}
	if old != nil && old.ActionID == input.ActionID {
		if old.Action != action {
			return nil, errors.New("action_id_conflict")
		}
		return old, nil
	}
	if xrayUpgradeActive(old) {
		return nil, ErrBusy
	}
	install, err := e.Store.Load()
	if err != nil {
		return nil, err
	}
	if install != nil && !terminal(install.State) {
		return nil, ErrBusy
	}
	if err = e.preflight(ctx, ""); err != nil {
		return nil, err
	}
	var s *XrayUpgrade
	switch action {
	case "apply":
		if input.OperationID != "" || (old != nil && old.State == "failed") {
			return nil, ErrBusy
		}
		i, current, err := e.currentXray(ctx)
		if err != nil {
			return nil, err
		}
		latest, err := latestXrayVersion(ctx, nil)
		if err != nil {
			return nil, err
		}
		if !xrayVersionNewer(latest, current) {
			return nil, errors.New("xray_already_current")
		}
		s = &XrayUpgrade{ID: ID(), Version: latest, PreviousVersion: current, Binary: i.Binary, Config: i.Config, Assets: i.AssetDir}
	case "retry", "rollback":
		if old == nil || old.ID != input.OperationID || old.State != "failed" {
			return nil, ErrBusy
		}
		if action == "rollback" && !old.BackupReady {
			return nil, errors.New("snapshot_unavailable")
		}
		s = old
	default:
		return nil, errors.New("invalid Xray update action")
	}
	s.ActionID = input.ActionID
	s.Action = action
	s.State = "launching"
	s.Stage = "launching"
	s.Error = ""
	if err = e.saveXrayUpgrade(s); err != nil {
		return nil, err
	}
	err = e.command(ctx, "/usr/bin/systemd-run", "--unit="+xrayUpgradeUnit(s.ActionID), "--collect", "--no-block", "--property=Type=exec", "--property=UMask=0077", "--property=RuntimeMaxSec=1200", e.InstallerSource, "-config", e.ConfigPath, "xray-upgrade", "--worker", s.ID, s.ActionID)
	if err != nil {
		s.State = "failed"
		s.Error = "worker_launch_failed"
		_ = e.saveXrayUpgrade(s)
	}
	return s, err
}

// RunXrayUpgrade runs in an unconstrained transient installer unit. The daemon
// remains confined: it cannot write Xray's executable. Stop it only during the
// critical section, then restart it even on failure so recovery stays usable.
func (e *Engine) RunXrayUpgrade(ctx context.Context, id, actionID string) (result error) {
	if requiredID(id) != nil || requiredID(actionID) != nil {
		return errors.New("invalid worker id")
	}
	unlock, err := Lock(filepath.Join(e.Store.Root, "operation.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	s, err := e.xrayUpgradeState()
	if err != nil {
		return err
	}
	if s == nil || s.ID != id || s.ActionID != actionID || s.State != "launching" {
		return errors.New("stale_attempt")
	}
	dir := e.xrayUpgradeDir(s)
	if err = secureDir(dir); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "operation.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	stage := func(name string) error {
		s.State = "running"
		s.Stage = name
		event := time.Now().Format(time.RFC3339) + " " + name
		s.Events = append(s.Events, event)
		fmt.Fprintln(logFile, event)
		return e.saveXrayUpgrade(s)
	}
	defer func() {
		if result != nil {
			s.State = "failed"
			s.Error = errorCode(result)
			s.Events = append(s.Events, time.Now().Format(time.RFC3339)+" "+s.Error)
			fmt.Fprintf(logFile, "%s failed: %s\n", time.Now().Format(time.RFC3339), s.Error)
		} else {
			s.State = "succeeded"
			if s.Action == "rollback" {
				s.State = "rolled_back"
			}
			s.Error = ""
		}
		if err := e.saveXrayUpgrade(s); result == nil {
			result = err
		}
	}()
	if s.Action != "rollback" && !s.Changed {
		if err = stage("download"); err != nil {
			return err
		}
		download := e.XrayDownload
		if download == nil {
			download = downloadXrayBinary
		}
		hash, err := download(ctx, s.Version, filepath.Join(dir, "candidate"))
		if err != nil {
			return err
		}
		s.CandidateHash = hash
	}
	if err = stage("stop_helper"); err != nil {
		return err
	}
	if err = e.unit(ctx, "stop", "xray-core-helper.service"); err != nil {
		return err
	}
	defer func() {
		restartCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := e.unit(restartCtx, "start", "xray-core-helper.service"); err != nil && result == nil {
			result = errors.New("helper_restart_failed")
		}
	}()
	release, err := Lock(filepath.Join(e.Store.Root, "runtime.lock"))
	if err != nil {
		return err
	}
	defer release()
	// Recheck pending mutations after the daemon has released ownership.
	if err = e.preflight(ctx, ""); err != nil {
		return err
	}
	i, err := e.resolveUpgradeXray(ctx)
	if err != nil {
		return err
	}
	if i.Binary != s.Binary || i.Config != s.Config || i.AssetDir != s.Assets {
		return errors.New("xray_installation_changed")
	}
	if s.Action == "rollback" {
		if err = stage("restore_binary"); err != nil {
			return err
		}
		if err = replaceXrayBinary(filepath.Join(dir, "previous"), s.Binary, s.BackupHash); err != nil {
			return err
		}
	} else if !s.Changed {
		if err = stage("validate"); err != nil {
			return err
		}
		before, err := fileHash(s.Config)
		if err != nil {
			return err
		}
		s.ConfigHash = before
		if err = validateNewXray(ctx, filepath.Join(dir, "candidate"), s.Config, s.Assets, logFile); err != nil {
			return err
		}
		after, err := fileHash(s.Config)
		if err != nil || before != after {
			return errors.New("xray_config_changed")
		}
		if !s.BackupReady {
			if err = stage("backup"); err != nil {
				return err
			}
			hash, err := fileHash(s.Binary)
			if err != nil {
				return err
			}
			if err = replaceXrayBinary(s.Binary, filepath.Join(dir, "previous"), hash); err != nil {
				return err
			}
			s.BackupHash = hash
			s.BackupReady = true
		}
		// Persist possible replacement before writing: interruption must always
		// offer restoration even if the final journal write did not complete.
		s.Changed = true
		if err = stage("replace_binary"); err != nil {
			return err
		}
		if err = replaceXrayBinary(filepath.Join(dir, "candidate"), s.Binary, s.CandidateHash); err != nil {
			return err
		}
	}
	if s.Action == "retry" && s.Changed {
		if err = replaceXrayBinary(filepath.Join(dir, "candidate"), s.Binary, s.CandidateHash); err != nil {
			return err
		}
	}
	if err = stage("restart_xray"); err != nil {
		return err
	}
	if err = e.unit(ctx, "restart", "xray.service"); err != nil {
		return errors.New("xray_restart_failed")
	}
	if err = e.command(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "xray.service"); err != nil {
		return errors.New("xray_not_active")
	}
	s.Stage = "completed"
	return nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func replaceXrayBinary(source, target, expected string) error {
	if expected == "" {
		return errors.New("xray_binary_hash_missing")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	out, err := os.CreateTemp(filepath.Dir(target), ".xray-upgrade-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	mode := os.FileMode(0755)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			if err = out.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
				out.Close()
				return err
			}
		}
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), input)
	if err == nil && hex.EncodeToString(h.Sum(nil)) != expected {
		err = errors.New("xray_binary_checksum_failed")
	}
	if err == nil {
		err = out.Chmod(mode)
	}
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(out.Name(), target)
}
func validateNewXray(ctx context.Context, binary, config, assets string, log io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	validator, err := transaction.NewXrayValidator(binary, assets)
	if err == nil {
		err = validator.Validate(ctx, config)
	}
	if err != nil {
		fmt.Fprintln(log, "New Xray configuration validation failed")
		return errors.New("xray_config_validation_failed")
	}
	return nil
}
func downloadXrayBinary(ctx context.Context, version, target string) (string, error) {
	name := "Xray-linux-64.zip"
	if runtime.GOARCH == "arm64" {
		name = "Xray-linux-arm64-v8a.zip"
	} else if runtime.GOARCH != "amd64" {
		return "", errors.New("unsupported release architecture")
	}
	base := "https://github.com/XTLS/Xray-core/releases/download/" + version + "/" + name
	client := &http.Client{Timeout: 10 * time.Minute}
	dgst, err := xrayDownload(ctx, client, base+".dgst")
	if err != nil {
		return "", errors.New("xray_checksum_download_failed")
	}
	expected, err := xrayArchiveSHA256(string(dgst))
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", errors.New("xray_download_failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", errors.New("xray_download_failed")
	}
	archive, err := os.CreateTemp(filepath.Dir(target), "archive-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	const limit = 256 << 20
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(archive, h), io.LimitReader(response.Body, limit+1))
	if err != nil || n > limit {
		return "", errors.New("xray_archive_download_failed")
	}
	if hex.EncodeToString(h.Sum(nil)) != expected {
		return "", errors.New("xray_archive_checksum_failed")
	}
	z, err := zip.NewReader(archive, n)
	if err != nil {
		return "", errors.New("xray_archive_invalid")
	}
	return extractXrayBinary(z, target)
}
func xrayArchiveSHA256(raw string) (string, error) {
	for _, line := range strings.Split(raw, "\n") {
		label, value, ok := strings.Cut(line, "=")
		if !ok || !strings.Contains(label, "256") {
			continue
		}
		value = strings.TrimSpace(value)
		decoded, err := hex.DecodeString(value)
		if err == nil && len(decoded) == sha256.Size {
			return strings.ToLower(value), nil
		}
	}
	return "", errors.New("xray_checksum_invalid")
}
func extractXrayBinary(z *zip.Reader, target string) (string, error) {
	var binary *zip.File
	for _, f := range z.File {
		if f.Name == "xray" {
			if binary != nil || !f.Mode().IsRegular() || f.UncompressedSize64 > 256<<20 {
				return "", errors.New("xray_archive_invalid")
			}
			binary = f
		}
	}
	if binary == nil {
		return "", errors.New("xray_archive_invalid")
	}
	input, err := binary.Open()
	if err != nil {
		return "", err
	}
	defer input.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), io.LimitReader(input, (256<<20)+1))
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
