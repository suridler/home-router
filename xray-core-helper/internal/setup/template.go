package setup

import (
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/xrayconfig"
)

func (s *fileStore) templateValues(plan Plan) (xrayconfig.TemplateValues, error) {
	config := s.config
	if s.configPath != "" {
		var err error
		config, err = bootstrap.Load(s.configPath)
		if err != nil {
			return xrayconfig.TemplateValues{}, err
		}
		if config.Network != s.network || config.Geodata.UserGeosite != s.userGeosite || config.Geodata.UserGeoip != s.userGeoip {
			return xrayconfig.TemplateValues{}, ErrRecoveryRequired
		}
	}
	config.Network = s.network
	config.Network.Interface, config.Network.Address, config.Network.IPv4Prefix = plan.Interface, plan.ListenAddress, plan.LANNetwork
	config.Geodata.UserGeosite = s.userGeosite
	config.Geodata.UserGeoip = s.userGeoip
	return setupTemplateValues(config), nil
}

func setupTemplateValues(config bootstrap.Config) xrayconfig.TemplateValues {
	return xrayconfig.TemplateValues{
		Network: config.Network, Logging: config.Logging, API: config.Xray.API,
		UserGeosite: config.Geodata.UserGeosite, UserGeoip: config.Geodata.UserGeoip,
	}
}

func (t *fileTransaction) templateValues() (xrayconfig.TemplateValues, error) {
	config := t.store.config
	path := t.store.configPath
	if t.manifest.Network != nil {
		path = t.manifest.Network.Candidate
	}
	if path != "" {
		var err error
		config, err = bootstrap.LoadWithTemplate(path, bootstrap.TemplateFile)
		if err != nil {
			return xrayconfig.TemplateValues{}, err
		}
	}
	config.Network = t.network
	config.Geodata.UserGeosite = t.store.userGeosite
	config.Geodata.UserGeoip = t.store.userGeoip
	return setupTemplateValues(config), nil
}
