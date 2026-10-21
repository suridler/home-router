// Package testconfig provides source-tree TOML fixtures for isolated tests.
// Production code must use bootstrap's explicit file-based loaders instead.
package testconfig

import (
	"path/filepath"
	"runtime"

	"home-router/xray-core-helper/internal/bootstrap"
)

func TemplatePath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "../../templConf/config.toml")
}

func Defaults() bootstrap.Config {
	config, err := bootstrap.ReadDefaults(TemplatePath())
	if err != nil {
		panic(err)
	}
	config.Xray.Binary = "/usr/local/bin/xray"
	config.Xray.AssetDir = "/usr/local/share/xray"
	return config
}

// Tests use the real text template without touching host files.
func init() { bootstrap.TemplateFile = TemplatePath() }

func Load(path string) (bootstrap.Config, error) {
	return bootstrap.LoadWithTemplate(path, TemplatePath())
}
