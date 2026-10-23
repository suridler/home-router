package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const xrayLatestURL = "https://api.github.com/repos/XTLS/Xray-core/releases/latest"
const xrayInstallerURL = "https://raw.githubusercontent.com/XTLS/Xray-install/main/install-release.sh"

var xrayStableVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// EnsureXrayInstalled is used only by the standalone install command. A service
// discovery error is never interpreted as permission to replace an installation.
// Newly installed Xray is a prerequisite, not part of helper's rollback snapshot.
func (e *Engine) EnsureXrayInstalled(ctx context.Context, op *Operation, confirm func() bool, out io.Writer) error {
	return e.ensureXrayInstalled(ctx, op, confirm, out, &http.Client{Timeout: 45 * time.Second}, runXrayInstaller)
}

type xrayScriptRunner func(context.Context, string, string, string) error

func (e *Engine) ensureXrayInstalled(ctx context.Context, op *Operation, confirm func() bool, out io.Writer, client *http.Client, run xrayScriptRunner) error {
	raw, err := e.Runner.Run(ctx, "/usr/bin/systemctl", "show", "xray.service", "--property=LoadState", "--value", "--no-pager")
	if err != nil {
		return errors.New("xray_detection_failed: cannot read xray.service")
	}
	state := strings.TrimSpace(string(raw))
	if !op.XrayInstallStarted {
		if state != "not-found" {
			// Loaded, masked and broken units all belong to the administrator.
			if state == "" {
				return errors.New("xray_detection_failed: empty LoadState")
			}
			return nil
		}
		// A binary or residual configuration is a partial/custom installation; do
		// not let the vendor installer replace it as if the host were empty.
		paths := []string{"/usr/local/bin/xray", "/usr/bin/xray", "/usr/local/etc/xray", "/etc/systemd/system/xray.service.d"}
		if e.Files.SystemRoot == "/" {
			if binary, lookupErr := exec.LookPath("xray"); lookupErr == nil {
				paths = append(paths, binary)
			}
		}
		for _, path := range paths {
			if _, statErr := os.Lstat(e.Files.Path(path)); !os.IsNotExist(statErr) {
				return errors.New("xray_partial_installation: Xray files exist without xray.service; configure the existing installation first")
			}
		}
		if confirm == nil || !confirm() {
			return errors.New("xray_install_declined: Xray-core is required")
		}
	}
	dir := e.Store.Dir(op.ID)
	logPath := filepath.Join(dir, "xray-install.log")
	fmt.Fprintf(out, "Установка Xray-core: журнал %s\n", logPath)
	if op.XrayVersion == "" {
		version, err := latestXrayVersion(ctx, client)
		if err != nil {
			return err
		}
		op.XrayVersion = version
		if err = e.Store.Save(op); err != nil {
			return err
		}
	}
	if !xrayStableVersion.MatchString(op.XrayVersion) {
		return errors.New("xray_release_invalid")
	}
	script, err := xrayDownload(ctx, client, xrayInstallerURL)
	if err != nil {
		return errors.New("xray_installer_download_failed")
	}
	scriptPath := filepath.Join(dir, "install-xray-release.sh")
	if err = atomicWrite(scriptPath, script, 0600); err != nil {
		return err
	}
	// Record ownership of this installation before invoking the vendor script;
	// retry may then finish its partially created service using the same version.
	op.XrayInstallStarted = true
	if err = e.Store.Save(op); err != nil {
		return err
	}
	fmt.Fprintf(out, "Xray-core %s; пользователь xray; ротация журналов в 04:00.\n", op.XrayVersion)
	if err = run(ctx, scriptPath, op.XrayVersion, logPath); err != nil {
		return fmt.Errorf("xray_install_failed: inspect %s", logPath)
	}
	// The vendor service has no explicit asset location. Add it only for this
	// new installation; discovery of existing installations stays unchanged.
	assets := e.Files.Path("/etc/systemd/system/xray.service.d/20-assets.conf")
	const content = "[Service]\nEnvironment=XRAY_LOCATION_ASSET=/usr/local/share/xray\n"
	if existing, readErr := os.ReadFile(assets); readErr == nil && string(existing) != content {
		return errors.New("xray_assets_conflict: existing 20-assets.conf was preserved")
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if err = atomicWrite(assets, []byte(content), 0644); err != nil {
		return err
	}
	if err = e.command(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return err
	}
	op.XrayInstallStarted = false
	if err = e.Store.Save(op); err != nil {
		return err
	}
	fmt.Fprintln(out, "Xray-core установлен. Настройка TProxy выполняется отдельно в setup.")
	return nil
}

func xrayDownload(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "xray-core-helper-installer")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("download failed")
	}
	const limit = 2 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit {
		return nil, errors.New("download incomplete or too large")
	}
	return raw, nil
}

// The vendor installer may download large assets and install OS dependencies;
// the ordinary two-minute API command runner is deliberately not used here.
// Bash executes the downloaded script file, never generated shell text.
func runXrayInstaller(ctx context.Context, script, version, logPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	fmt.Fprintf(log, "\n%s Xray installation %s\n", time.Now().Format(time.RFC3339), version)
	cmd := exec.CommandContext(ctx, "/bin/bash", script, "install", "--version", version, "--logrotate", "04:00:00")
	// Do not inherit custom vendor paths from the invoking user's environment.
	cmd.Env = append(os.Environ(), "DAT_PATH=/usr/local/share/xray", "JSON_PATH=/usr/local/etc/xray", "JSONS_PATH=")
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	fmt.Fprintf(log, "%s Xray installer finished: %v\n", time.Now().Format(time.RFC3339), err)
	return err
}
