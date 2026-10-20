package setup

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/commandrunner"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
	"home-router/xray-core-helper/internal/xrayservice"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type PreflightError struct{ Code string }

func (e *PreflightError) Error() string { return e.Code }

type preflight struct {
	lifecycle   func(context.Context) error
	networkIdle func(context.Context) error
	xrayBinary  string
	binary      func(string) error
	capability  func(context.Context) error
	listener    func(context.Context, string, string) error
	asset       func(string) error
}

func systemPreflight(assetDir string, binaries ...string) *preflight {
	binary := "/usr/local/bin/xray"
	if len(binaries) > 0 {
		binary = binaries[0]
	}
	return &preflight{
		xrayBinary: binary,
		lifecycle:  xrayservice.New().CheckGatewayLifecycle,
		networkIdle: func(ctx context.Context) error {
			runner, _ := commandrunner.New("/usr/bin/systemctl", 10*time.Second)
			result, err := runner.Run(ctx, []string{"show", "xray-tproxy-routing.service", "--property=ActiveState,LoadState"})
			text := string(result.Stdout())
			if strings.Contains(text, "ActiveState=inactive\n") || strings.Contains(text, "ActiveState=failed\n") {
				return nil
			}
			_ = err
			return errors.New("network must be inactive before preparation")
		},
		binary: func(path string) error {
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return errors.New("not executable")
			}
			return nil
		},
		capability: func(ctx context.Context) error {
			runner, _ := commandrunner.New(binary, 10*time.Second)
			result, err := runner.Run(ctx, []string{"help", "api"})
			if err != nil {
				return err
			}
			text := string(result.Stdout())
			for _, command := range []string{"lsrules", "adrules", "ado", "rmo"} {
				if !strings.Contains(text, command) {
					return errors.New("missing capability")
				}
			}
			return nil
		},
		listener: func(ctx context.Context, network, address string) error {
			var lc net.ListenConfig
			if network == "udp" {
				conn, err := lc.ListenPacket(ctx, "udp4", address)
				if err == nil {
					conn.Close()
				}
				return err
			}
			conn, err := lc.Listen(ctx, "tcp4", address)
			if err == nil {
				conn.Close()
			}
			return err
		},
		asset: func(name string) error {
			path := filepath.Join(assetDir, name)
			if err := safePath(path); err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() == 0 {
				return errors.New("asset unavailable")
			}
			return nil
		},
	}
}

func (p *preflight) check(ctx context.Context, plan Plan, raw []byte) error {
	fail := func(code string) error { return &PreflightError{Code: "preflight_" + code} }
	for _, binary := range []string{p.xrayBinary, "/usr/bin/systemctl", "/usr/bin/systemd-analyze", "/usr/sbin/nft", "/usr/sbin/sysctl", "/sbin/ip"} {
		if p.binary(binary) != nil {
			return fail("binary_unavailable")
		}
	}
	if p.capability(ctx) != nil {
		return fail("xray_incompatible")
	}
	dependencies, err := xrayconfig.NativeAssetDependencies(raw)
	if err != nil {
		return fail("candidate_invalid")
	}
	for _, name := range dependencies {
		if p.asset(name) != nil {
			return fail("asset_unavailable")
		}
	}
	listeners, err := xrayconfig.NativeIPv4Listeners(raw)
	if err != nil {
		return fail("candidate_invalid")
	}
	for _, listener := range listeners {
		if p.listener(ctx, listener.Network, listener.Address) != nil {
			return fail("port_in_use")
		}
	}
	return ctx.Err()
}
func (s *fileStore) Preflight(ctx context.Context, plan Plan) error {
	if s.preflight == nil {
		return nil
	}
	if s.preflight.lifecycle != nil && s.preflight.lifecycle(ctx) != nil {
		return &PreflightError{Code: "preflight_xray_lifecycle_conflict"}
	}
	if s.preflight.networkIdle != nil && s.preflight.networkIdle(ctx) != nil {
		return &PreflightError{Code: "preflight_network_must_be_inactive"}
	}
	for _, entry := range s.entries {
		if entry.ID == templatecatalog.XrayConfig {
			raw, _, err := readSafe(entry.WorkingPath)
			if err != nil {
				return &PreflightError{Code: "preflight_candidate_unavailable"}
			}
			if plan.Gateway != nil {
				candidate, _, err := s.gatewayCandidate(plan)
				if err != nil {
					return err
				}
				prior, _, _ := readSafe(entry.SystemPath)
				owned := map[xrayconfig.NativeListener]bool{}
				listeners, _ := xrayconfig.NativeInboundListeners(prior)
				for _, item := range listeners {
					owned[item] = true
				}
				if endpoint, err := xrayconfig.ExtractLocalAPI(prior); err == nil {
					owned[xrayconfig.NativeListener{Network: "tcp", Address: endpoint}] = true
				}
				probe := *s.preflight
				if s.assetDir != "" {
					probe.asset = func(name string) error {
						if name == s.userGeosite {
							_, err := s.readSetupAsset(s.geositePath())
							if errors.Is(err, os.ErrNotExist) {
								return s.preflight.binary(s.geositeBuilder)
							}
						}
						return s.preflight.asset(name)
					}
				}
				probe.listener = func(ctx context.Context, network, address string) error {
					if owned[xrayconfig.NativeListener{Network: network, Address: address}] {
						return nil
					}
					return s.preflight.listener(ctx, network, address)
				}
				return probe.check(ctx, plan, candidate)
			}
			if plan.PreserveXray {
				raw, _, err = readSafe(entry.SystemPath)
				if err != nil {
					return &PreflightError{Code: "preflight_candidate_unavailable"}
				}
				// Existing Xray may own its listeners; only validate its syntax here.
				return s.validate(ctx, entry.ID, entry.SystemPath)
			}
			return s.preflight.check(ctx, plan, raw)
		}
	}
	return &PreflightError{Code: "preflight_candidate_unavailable"}
}
