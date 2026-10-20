// Package gatewaycheck implements explicit read-only gateway diagnostics.
// It is never a prerequisite for native lifecycle or service-control actions.
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
		return stageError("installation_discovery")
	}
	if xrayservice.New().CheckGatewayLifecycle(ctx) != nil {
		return stageError("lifecycle_ordering")
	}
	raw, err := read(installation.Config)
	if err != nil {
		return stageError("config_read")
	}
	gateway, err := xrayconfig.InspectGateway(raw)
	if err != nil {
		return stageError("gateway_config")
	}
	catalog, err := templatecatalog.ForXrayConfig(installation.Config)
	if err != nil {
		return stageError("template_catalog")
	}
	docs := map[string]*nativebinding.Document{}
	for _, id := range []templatecatalog.ID{templatecatalog.Nftables, templatecatalog.TProxyUnit} {
		entry, _ := catalog.Lookup(id)
		content, err := read(entry.SystemPath)
		if err != nil {
			return stageError("native_read")
		}
		doc, err := nativebinding.Parse(content)
		if err != nil {
			return stageError("native_bindings")
		}
		docs[string(id)] = doc
	}
	if doc := docs[string(templatecatalog.Nftables)]; doc != nil {
		if value, err := doc.Scalar("intercept"); err == nil && value == strconv.Itoa(gateway.EgressMark) {
			return stageError("intercept_mark_conflict")
		}
	}
	if checkDeclarations(ctx, docs, raw, configPath, xrayidentity.New().GID) != nil {
		return stageError("binding_consistency")
	}

	validator, err := transaction.NewXrayValidator(installation.Binary, installation.AssetDir)
	if err != nil {
		return stageError("validator_configuration")
	}
	if validator.Validate(ctx, installation.Config) != nil {
		return stageError("config_validation")
	}
	ctl, err := commandrunner.New("/usr/bin/systemctl", 10*time.Second)
	if err != nil {
		return stageError("service_query_configuration")
	}
	status, err := ctl.Run(ctx, []string{"show", "xray.service", "--property=ActiveState,MainPID"})
	if err != nil {
		return stageError("service_query")
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
		return stageError("service_inactive")
	}
	base := filepath.Join("/proc", strconv.Itoa(pid))
	executable, err := os.Readlink(filepath.Join(base, "exe"))
	if err != nil || executable != installation.Binary {
		return stageError("process_executable")
	}
	command, err := read(filepath.Join(base, "cmdline"))
	if err != nil {
		return stageError("process_arguments_read")
	}
	found := false
	for _, arg := range strings.Split(string(command), "\x00") {
		if arg == installation.Config || arg == "-config="+installation.Config || arg == "--config="+installation.Config || arg == "-c="+installation.Config {
			found = true
		}
	}
	if !found {
		return stageError("process_arguments")
	}
	fds, err := os.ReadDir(filepath.Join(base, "fd"))
	if err != nil {
		return stageError("process_sockets_read")
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
			return stageError("tproxy_" + network + "_listener")
		}
	}
	endpoint, err := xrayconfig.ExtractLocalAPI(raw)
	if err != nil {
		return stageError("api_configuration")
	}
	api, err := routingservice.NewClient(installation.Binary, endpoint, nil)
	if err != nil {
		return stageError("api_client")
	}
	if _, err = api.ListRules(ctx); err != nil {
		return stageError("api_unavailable")
	}
	// Recheck pid and configuration after all observations.
	again, err := ctl.Run(ctx, []string{"show", "xray.service", "--property=MainPID", "--value"})
	if err != nil || strings.TrimSpace(string(again.Stdout())) != strconv.Itoa(pid) {
		return stageError("process_changed")
	}
	after, err := read(installation.Config)
	if err != nil || string(after) != string(raw) {
		return stageError("config_changed")
	}
	return nil
}

// checkDeclarations compares declared bindings for explicit diagnostics only.
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
		return stageError("binding_consistency")
	}
	for _, check := range checks {
		if check.Status == "mismatch" || check.Status == "unresolved" {
			return stageError("binding_consistency")
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
		return nil, stageError("input_read")
	}
	return raw, nil
}

// Wait is retained for command callers. Diagnostics run once and report the
// observed failure instead of repeatedly validating the entire configuration.
func Wait(ctx context.Context, configPaths ...string) error {
	return Check(ctx, configPaths...)
}

// stageError deliberately excludes raw errors, command output and file contents.
func stageError(stage string) error {
	return fmt.Errorf("%w: %s", ErrNotReady, stage)
}
