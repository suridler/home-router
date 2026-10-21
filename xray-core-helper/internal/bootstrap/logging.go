package bootstrap

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// LoggingConfig provides explicit template inputs. Loading these settings never
// creates log files or changes the running Xray configuration.
type LoggingConfig struct {
	AccessFilePath string `toml:"access_file_path"`
	ErrorFilePath  string `toml:"error_file_path"`
	AccessPath     string `toml:"access_path"`
	ErrorPath      string `toml:"error_path"`
	Level          string `toml:"level"`
	DNSLog         bool   `toml:"dns_log"`
}

func (config LoggingConfig) Validate() error {
	if !ValidLogDestination(config.AccessPath) || !ValidLogDestination(config.ErrorPath) {
		return errors.New("logging destinations must be empty, none, or clean absolute file paths")
	}
	for _, path := range []string{config.AccessFilePath, config.ErrorFilePath} {
		if path != "" && (path == "none" || !ValidLogDestination(path)) {
			return errors.New("logging file destinations must be clean absolute file paths")
		}
	}

	if !ValidLogLevel(config.Level) {
		return errors.New("logging.level must be debug, info, warning, error, or none")
	}
	return nil
}

func ValidLogLevel(value string) bool {
	switch value {
	case "debug", "info", "warning", "error", "none":
		return true
	}
	return false
}

func ValidLogDestination(value string) bool {
	if value == "" || value == "none" {
		return true
	}
	if len(value) > 4096 || !utf8.ValidString(value) || !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" {
		return false
	}
	return !strings.ContainsFunc(value, func(c rune) bool { return c < 32 || c == 127 })
}
