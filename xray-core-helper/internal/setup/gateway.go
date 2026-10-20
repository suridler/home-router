package setup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
)

func (s *fileStore) gatewayCandidate(plan Plan) ([]byte, xrayconfig.Gateway, error) {
	for _, entry := range s.entries {
		if entry.ID != templatecatalog.XrayConfig {
			continue
		}
		defaults, _, err := readSafe(entry.WorkingPath)
		if err != nil {
			return nil, xrayconfig.Gateway{}, err
		}
		source, _, err := readSafe(entry.SystemPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, xrayconfig.Gateway{}, err
		}
		if plan.ConfigFingerprint != "" && contentHash(source) != plan.ConfigFingerprint {
			return nil, xrayconfig.Gateway{}, ErrRecoveryRequired
		}
		inputs, err := s.templateValues(plan)
		if err != nil {
			return nil, xrayconfig.Gateway{}, err
		}
		projection := source
		if plan.ReplaceXray {
			projection = nil
		}
		defaults, err = xrayconfig.RenderSetupTemplate(defaults, projection, inputs)
		if err != nil {
			return nil, xrayconfig.Gateway{}, err
		}
		if plan.ReplaceXray || len(bytes.TrimSpace(source)) == 0 {
			source = defaults
		}
		inputs.Xray = defaults
		source, err = xrayconfig.RenderTemplate(source, inputs)
		if err != nil {
			return nil, xrayconfig.Gateway{}, err
		}
		candidate, gateway, err := xrayconfig.PrepareGateway(source, defaults)
		if err != nil {
			return nil, xrayconfig.Gateway{}, err
		}
		candidate, changed, err := xrayconfig.PrepareUserGeosite(candidate, s.userGeosite)
		if err != nil {
			return nil, xrayconfig.Gateway{}, err
		}
		if changed {
			gateway.Changes = append(gateway.Changes, "routing.proxy-domains-permanent")
		}
		return candidate, gateway, nil
	}
	return nil, xrayconfig.Gateway{}, xrayconfig.ErrGateway
}

func (s *fileStore) AnalyzeGateway(ctx context.Context, plan Plan) (Plan, error) {
	if !s.checkRelations {
		return plan, nil
	} // hermetic native-store adapters need no gateway profile
	references, err := s.planReferences()
	if err != nil {
		return Plan{}, err
	}
	plan.NativeFingerprints = references
	candidate, gateway, err := s.gatewayCandidate(plan)
	if err != nil {
		return Plan{}, &PreflightError{Code: "preflight_xray_gateway_review_required"}
	}
	plan.Gateway = &gateway
	plan.XrayCandidateHash = contentHash(candidate)
	for _, entry := range s.entries {
		if entry.ID == templatecatalog.XrayConfig {
			prior, _, err := readSafe(entry.SystemPath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return Plan{}, err
			}
			plan.ConfigFingerprint = contentHash(prior)
			plan.PreserveXray = bytes.Equal(prior, candidate)
		}
	}
	action, runtime := "complete", "restart_xray"
	if plan.ReplaceXray {
		action = "replace_from_template"
	}
	if plan.PreserveXray {
		action, runtime = "preserve", "ensure_xray"
	}
	if len(plan.Artifacts) == 0 {
		plan.Artifacts = []Artifact{{"xray-config", action, runtime}}
	} else {
		plan.Artifacts[0] = Artifact{"xray-config", action, runtime}
	}
	return plan, ctx.Err()
}

func (s *fileStore) planReferences() (map[string]string, error) {
	values := map[string]string{}
	if s.configPath != "" {
		raw, _, err := readSafe(s.configPath)
		if err != nil {
			return nil, err
		}
		values["helper-config"] = contentHash(raw)
	}
	for _, entry := range s.entries {
		for side, path := range map[string]string{"working": entry.WorkingPath, "system": entry.SystemPath} {
			raw, _, err := readSafe(path)
			value := contentHash(raw)
			if errors.Is(err, os.ErrNotExist) {
				value = "absent"
			} else if err != nil {
				return nil, err
			}
			values[string(entry.ID)+"."+side] = value
		}
	}
	if s.assetDir != "" {
		for _, asset := range []struct{ key, name string }{{"user-geosite", s.userGeosite}, {"user-geoip", s.userGeoip}} {
			path := filepath.Join(s.assetDir, asset.name)
			raw, err := s.readSetupAsset(path)
			value := contentHash(raw)
			if errors.Is(err, os.ErrNotExist) {
				value = "absent"
			} else if err != nil {
				return nil, err
			}
			values[asset.key+":"+path] = value
		}
	}
	return values, nil
}
func (s *fileStore) checkPlanReferences(plan Plan) error {
	if plan.NativeFingerprints == nil {
		return nil
	}
	current, err := s.planReferences()
	if err != nil {
		return err
	}
	if len(current) != len(plan.NativeFingerprints) {
		return ErrRecoveryRequired
	}
	for key, value := range current {
		if plan.NativeFingerprints[key] != value {
			return ErrRecoveryRequired
		}
	}
	return nil
}
