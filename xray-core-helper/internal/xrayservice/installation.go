package xrayservice

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Installation is a bounded projection, never raw arguments or environment.
type Installation struct {
	Unit     string `json:"unit"`
	Binary   string `json:"binary"`
	Config   string `json:"config"`
	AssetDir string `json:"assetDir"`
}

var ErrInstallation = errors.New("xray_installation_unavailable: require trusted direct ExecStart and explicit XRAY_LOCATION_ASSET; unsupported environment or paths")

// Resolve reads effective unit properties without starting either service.
// Only a conservative subset of systemd environment syntax is supported.
// Unresolved environment sources fail closed instead of guessing Xray defaults.
func (r *Resolver) Resolve(ctx context.Context) (Installation, error) {
	path, err := r.ConfigPath(ctx, "")
	if err != nil {
		return Installation{}, err
	}
	property := func(name string) []string {
		return []string{"get-property", "org.freedesktop.systemd1", "/org/freedesktop/systemd1/unit/xray_2eservice", "org.freedesktop.systemd1.Service", name}
	}
	var commands [][]any
	if r.query(ctx, property("ExecStart"), "a(sasbttttuii)", &commands) != nil || len(commands) != 1 || len(commands[0]) != 10 {
		return Installation{}, ErrInstallation
	}
	binary, ok := commands[0][0].(string)
	if !ok || !absolute(binary) {
		return Installation{}, ErrInstallation
	}
	argvValues, ok := commands[0][1].([]any)
	if !ok || len(argvValues) < 2 {
		return Installation{}, ErrInstallation
	}
	argv := make([]string, len(argvValues))
	for i, value := range argvValues {
		var ok bool
		argv[i], ok = value.(string)
		if !ok {
			return Installation{}, ErrInstallation
		}
	}
	secondPath, err := configArgument(argv[1:])
	if err != nil || argv[0] != binary || secondPath != path {
		return Installation{}, ErrInstallation
	}

	var env, pass, unset []string
	for name, destination := range map[string]*[]string{"Environment": &env, "PassEnvironment": &pass, "UnsetEnvironment": &unset} {
		if r.query(ctx, property(name), "as", destination) != nil {
			return Installation{}, ErrInstallation
		}
	}
	for _, value := range append(pass, unset...) {
		if value == "XRAY_LOCATION_ASSET" || strings.HasPrefix(value, "XRAY_LOCATION_ASSET=") {
			return Installation{}, ErrInstallation
		}
	}
	assets := ""
	for _, value := range env {
		if v, ok := strings.CutPrefix(value, "XRAY_LOCATION_ASSET="); ok {
			assets = v
		}
	}
	var files [][]any
	if r.query(ctx, property("EnvironmentFiles"), "a(sb)", &files) != nil {
		return Installation{}, ErrInstallation
	}
	for _, item := range files {
		if len(item) != 2 {
			return Installation{}, ErrInstallation
		}
		file, ok := item[0].(string)
		optional, good := item[1].(bool)
		if !ok || !good || !absolute(file) || strings.ContainsAny(file, "*?[") {
			return Installation{}, ErrInstallation
		}
		if err := TrustedPath(file, false); err != nil {
			return Installation{}, ErrInstallation
		}
		raw, err := readEnvironmentFile(file)
		if optional && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Installation{}, ErrInstallation
		}

		value, found, err := assetAssignment(string(raw))
		if err != nil {
			return Installation{}, err
		}
		if found {
			assets = value
		}
	}
	if !absolute(assets) {
		return Installation{}, ErrInstallation
	}
	return Installation{Unit: "xray.service", Binary: binary, Config: path, AssetDir: assets}, nil
}

func readEnvironmentFile(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, ErrInstallation
	}
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, ErrInstallation
	}
	return raw, nil
}

func absolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && !strings.ContainsAny(path, "$%\x00\r\n")
}

// assetAssignment deliberately refuses multiline and escaped environment-file
// forms. It never evaluates shell text or expands variables.
func assetAssignment(raw string) (string, bool, error) {
	value, found := "", false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.ContainsAny(line, "\\\r") {
			return "", false, ErrInstallation
		}
		key, text, ok := strings.Cut(line, "=")
		if !ok {
			return "", false, ErrInstallation
		}
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "\"") {
			var err error
			text, err = strconv.Unquote(text)
			if err != nil {
				return "", false, ErrInstallation
			}
		} else if strings.HasPrefix(text, "'") {
			if len(text) < 2 || !strings.HasSuffix(text, "'") {
				return "", false, ErrInstallation
			}
			text = text[1 : len(text)-1]
			if strings.Contains(text, "'") {
				return "", false, ErrInstallation
			}
		} else if strings.ContainsAny(text, "\"'") {
			return "", false, ErrInstallation
		}
		if strings.TrimSpace(key) != "XRAY_LOCATION_ASSET" {
			continue
		}
		if !absolute(text) {
			return "", false, ErrInstallation
		}
		value, found = text, true
	}
	return value, found, nil
}

// TrustedPath rejects symlinks and paths writable by non-root users. Missing
// leaf files are allowed for explicit first setup, but their parents must exist.
func TrustedPath(path string, executable bool) error {
	if !absolute(path) {
		return ErrInstallation
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && current == path && !executable {
			continue
		}
		if err != nil {
			return ErrInstallation
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return ErrInstallation
		}
		if current == path && executable && (!info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0) {
			return ErrInstallation
		}
		if current != path && !info.IsDir() {
			return ErrInstallation
		}
		if current == "/" {
			break
		}
	}
	return nil
}

func (installation Installation) CheckTrusted() error {
	if TrustedPath(installation.Binary, true) != nil || TrustedPath(installation.Config, false) != nil || TrustedPath(installation.AssetDir, false) != nil {
		return ErrInstallation
	}
	if info, err := os.Lstat(installation.Config); err == nil && !info.Mode().IsRegular() {
		return ErrInstallation
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrInstallation
	}
	info, err := os.Stat(installation.AssetDir)
	if err != nil || !info.IsDir() {
		return ErrInstallation
	}
	return nil
}
