// Package gatewaycheck implements the fixed read-only activation gate shared by
// interactive activation and the routing unit at boot. It never changes routing.
package gatewaycheck

import (
	"context"
	"errors"
	"fmt"
	"home-router/xray-core-helper/internal/routingservice"
	"home-router/xray-core-helper/internal/transaction"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/commandrunner"
	"home-router/xray-core-helper/internal/nativebinding"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
	"home-router/xray-core-helper/internal/xrayidentity"
	"home-router/xray-core-helper/internal/xrayservice"
)

var ErrNotReady = errors.New("xray_gateway_not_ready")

// Check validates the effective service, declared native relationships and
// socket ownership. It cannot prove external connectivity or LAN forwarding.
func Check(ctx context.Context, configPaths ...string) error {
	configPath := bootstrap.DefaultConfigPath
	if len(configPaths) > 0 {
		configPath = configPaths[0]
	}
	installation, err := xrayservice.New().Resolve(ctx)
	if err != nil {
		return ErrNotReady
	}
	if xrayservice.New().CheckGatewayLifecycle(ctx) != nil {
		return ErrNotReady
	}
	if installation.CheckTrusted() != nil {
		return ErrNotReady
	}
	raw, err := read(installation.Config)
	if err != nil {
		return ErrNotReady
	}
	gateway, err := xrayconfig.InspectGateway(raw)
	if err != nil {
		return ErrNotReady
	}
	catalog, err := templatecatalog.ForXrayConfig(installation.Config)
	if err != nil {
		return ErrNotReady
	}
	docs := map[string]*nativebinding.Document{}
	for _, id := range []templatecatalog.ID{templatecatalog.Nftables, templatecatalog.TProxyUnit} {
		entry, _ := catalog.Lookup(id)
		if xrayservice.TrustedPath(entry.SystemPath, false) != nil {
			return ErrNotReady
		}
		content, err := read(entry.SystemPath)
		if err != nil {
			return ErrNotReady
		}
		doc, err := nativebinding.Parse(content)
		if err != nil {
			return ErrNotReady
		}
		docs[string(id)] = doc
	}
	if doc := docs[string(templatecatalog.Nftables)]; doc != nil {
		if value, err := doc.Scalar("intercept"); err == nil && value == strconv.Itoa(gateway.EgressMark) {
			return ErrNotReady
		}
	}
	if checkDeclarations(ctx, docs, raw, configPath, xrayidentity.New().GID) != nil {
		return ErrNotReady
	}

	validator, err := transaction.NewXrayValidator(installation.Binary, installation.AssetDir)
	if err != nil {
		return ErrNotReady
	}
	if validator.Validate(ctx, installation.Config) != nil {
		return ErrNotReady
	}
	ctl, err := commandrunner.New("/usr/bin/systemctl", 10*time.Second)
	if err != nil {
		return ErrNotReady
	}
	status, err := ctl.Run(ctx, []string{"show", "xray.service", "--property=ActiveState,MainPID,ExecMainStartTimestampMonotonic"})
	if err != nil {
		return ErrNotReady
	}
	properties := map[string]string{}
	for _, line := range strings.Split(string(status.Stdout()), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			properties[key] = value
		}
	}
	pid, err := strconv.Atoi(properties["MainPID"])
	if err != nil || pid <= 0 || properties["ActiveState"] != "active" {
		return ErrNotReady
	}
	start, err := strconv.ParseUint(properties["ExecMainStartTimestampMonotonic"], 10, 64)
	if err != nil || start == 0 {
		return ErrNotReady
	}
	uptime, err := read("/proc/uptime")
	if err != nil {
		return ErrNotReady
	}
	fields := strings.Fields(string(uptime))
	if len(fields) == 0 {
		return ErrNotReady
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return ErrNotReady
	}
	info, err := os.Lstat(installation.Config)
	if err != nil {
		return ErrNotReady
	}
	started := time.Now().Add(-time.Duration(seconds * float64(time.Second))).Add(time.Duration(start) * time.Microsecond)
	// A newer boot file is not evidence of the configuration loaded by Xray.
	if info.ModTime().After(started) {
		return ErrNotReady
	}
	base := filepath.Join("/proc", strconv.Itoa(pid))
	executable, err := os.Readlink(filepath.Join(base, "exe"))
	if err != nil || executable != installation.Binary {
		return ErrNotReady
	}
	command, err := read(filepath.Join(base, "cmdline"))
	if err != nil {
		return ErrNotReady
	}
	found := false
	for _, arg := range strings.Split(string(command), "\x00") {
		if arg == installation.Config || arg == "-config="+installation.Config || arg == "--config="+installation.Config || arg == "-c="+installation.Config {
			found = true
		}
	}
	if !found {
		return ErrNotReady
	}
	fds, err := os.ReadDir(filepath.Join(base, "fd"))
	if err != nil {
		return ErrNotReady
	}
	inodes := map[string]bool{}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join(base, "fd", fd.Name()))
		if err == nil && strings.HasPrefix(target, "socket:[") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}
	for _, network := range []string{"tcp", "udp"} {
		matched := false
		for _, suffix := range []string{"", "6"} {
			table, err := read(filepath.Join(base, "net", network+suffix))
			if err != nil {
				continue
			}
			if ownsListener(table, inodes, gateway.Port, network) {
				matched = true
			}
		}
		if !matched {
			return ErrNotReady
		}
	}
	endpoint, err := xrayconfig.ExtractLocalAPI(raw)
	if err != nil {
		return ErrNotReady
	}
	api, err := routingservice.NewClient(installation.Binary, endpoint, nil)
	if err != nil {
		return ErrNotReady
	}
	if _, err = api.ListRules(ctx); err != nil {
		return ErrNotReady
	}
	// Recheck pid and configuration after all observations.
	again, err := ctl.Run(ctx, []string{"show", "xray.service", "--property=MainPID", "--value"})
	if err != nil || strings.TrimSpace(string(again.Stdout())) != strconv.Itoa(pid) {
		return ErrNotReady
	}
	after, err := read(installation.Config)
	if err != nil || string(after) != string(raw) {
		return ErrNotReady
	}
	return nil
}

// checkDeclarations is shared by activation and boot checks without mutating files.
func checkDeclarations(ctx context.Context, docs map[string]*nativebinding.Document, raw []byte, configPath string, identity func(context.Context) (int, error)) error {
	checks, err := nativebinding.CheckDocuments(docs, func(source string) (string, error) {
		if strings.HasPrefix(source, "helper.network.") {
			c, err := bootstrap.Load(configPath)
			if err != nil {
				return "", nativebinding.ErrUnresolved
			}
			return c.Network.BindingValue(source)
		}
		if source == "xray.service-gid" {
			gid, err := identity(ctx)
			return strconv.Itoa(gid), err
		}
		return xrayconfig.BindingValue(raw, source)
	})
	if err != nil {
		return ErrNotReady
	}
	for _, check := range checks {
		if check.Status == "mismatch" || check.Status == "unresolved" {
			return ErrNotReady
		}
	}
	return nil
}

func read(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// /proc files report size zero. The bounded reader also covers those files.
	return boundedRead(file)
}
func ownsListener(raw []byte, inodes map[string]bool, port int, network string) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		address, p, ok := strings.Cut(f[1], ":")
		if !ok || strings.Trim(address, "0") != "" || !strings.EqualFold(p, fmt.Sprintf("%04X", port)) {
			continue
		}
		if network == "tcp" && f[3] != "0A" || network == "udp" && f[3] != "07" {
			continue
		}
		if inodes[f[9]] {
			return true
		}
	}
	return false
}

func boundedRead(file *os.File) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return nil, ErrNotReady
	}
	return raw, nil
}

// Wait allows Type=simple services time to create their listeners after start.
func Wait(ctx context.Context, configPaths ...string) error {
	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		if Check(checkCtx, configPaths...) == nil {
			return nil
		}
		select {
		case <-checkCtx.Done():
			return ErrNotReady
		case <-time.After(500 * time.Millisecond):
		}
	}
}
