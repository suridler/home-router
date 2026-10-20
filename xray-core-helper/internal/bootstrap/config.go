package bootstrap

import (
	"fmt"
	"home-router/xray-core-helper/internal/geodatalimits"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const DefaultConfigPath = "/etc/xray-core-helper/config.toml"
const DefaultUserGeositeName = "geosite_user.dat"
const DefaultUserGeoipName = "geoip_user.dat"

const (
	geositeAssetName = "geosite.dat"
	geoipAssetName   = "geoip.dat"
	sha256Algorithm  = "sha256"
)

var systemGroupName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
var userGeositeName = regexp.MustCompile(`^geosite_[A-Za-z0-9][A-Za-z0-9_-]{0,47}\.dat$`)
var userGeoipName = regexp.MustCompile(`^geoip_[A-Za-z0-9][A-Za-z0-9_-]{0,47}\.dat$`)

type Config struct {
	Network      NetworkConfig      `toml:"network"`
	Logging      LoggingConfig      `toml:"logging"`
	Scheduler    SchedulerConfig    `toml:"scheduler"`
	Connectivity ConnectivityConfig `toml:"connectivity"`
	Backup       BackupConfig       `toml:"backup"`
	Xray         XrayConfig         `toml:"xray"`
	Geodata      GeodataConfig      `toml:"geodata"`
	Storage      StorageConfig      `toml:"storage"`
	Server       ServerConfig       `toml:"server"`
	Web          WebConfig          `toml:"web"`
}

type SchedulerConfig struct {
	GeodataUpdateEnabled bool   `toml:"geodata_update_enabled"`
	GeodataUpdateTime    string `toml:"geodata_update_time"`
}

func (config SchedulerConfig) Validate() error {
	if _, err := time.Parse("15:04", config.GeodataUpdateTime); err != nil || len(config.GeodataUpdateTime) != 5 {
		return fmt.Errorf("scheduler.geodata_update_time must be HH:MM in server local time")
	}
	return nil
}

type ConnectivityConfig struct {
	URL            string `toml:"url"`
	ExpectedStatus int    `toml:"expected_status"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
}

func (config ConnectivityConfig) Validate() error {
	u, err := url.Parse(config.URL)
	if err != nil || u == nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || len(config.URL) > 2048 {
		return fmt.Errorf("connectivity.url must be an HTTPS URL without credentials, query or fragment")
	}
	if u.Port() != "" && u.Port() != "443" {
		return fmt.Errorf("connectivity.url must use HTTPS port 443")
	}
	if config.ExpectedStatus < 200 || config.ExpectedStatus > 299 || config.TimeoutSeconds < 5 || config.TimeoutSeconds > 30 {
		return fmt.Errorf("connectivity expected_status must be 200..299 and timeout_seconds 5..30")
	}
	return nil
}

type WebConfig struct {
	ExporterURL    string `toml:"exporter_url" json:"exporterURL"`
	Host           string `toml:"host" json:"host"`
	Port           int    `toml:"port" json:"port"`
	ControlEnabled bool   `toml:"control_enabled" json:"controlEnabled"`
}

type BackupConfig struct {
	RetainPerTemplate int `toml:"retain_per_template"`
}

type XrayConfig struct {
	Binary string `toml:"-"`
	// Config is resolved from xray.service at daemon startup, never from TOML.
	Config   string `toml:"-"`
	API      string `toml:"api"`
	AssetDir string `toml:"-"`
}

type GeodataConfig struct {
	MaxAssetSizeMB int                 `toml:"max_asset_size_mb"`
	Origins        []string            `toml:"origins"`
	Builder        string              `toml:"builder"`
	UserGeosite    string              `toml:"user_geosite"`
	UserGeoip      string              `toml:"user_geoip"`
	Geosite        GeodataSourceConfig `toml:"geosite"`
	Geoip          GeodataSourceConfig `toml:"geoip"`
}

type GeodataSourceConfig struct {
	URL      string                `toml:"url"`
	Checksum GeodataChecksumConfig `toml:"checksum"`
}

type GeodataChecksumConfig struct {
	URL       string `toml:"url"`
	Algorithm string `toml:"algorithm"`
}

type StorageConfig struct {
	Database string `toml:"database"`
}

type ServerConfig struct {
	Socket      string `toml:"socket"`
	SocketGroup string `toml:"socket_group"`
}

func Defaults() Config {
	return Config{
		Network:      NetworkConfig{InterceptMark: 1, PolicyTable: 100},
		Logging:      LoggingConfig{AccessPath: "none", Level: "warning"},
		Scheduler:    SchedulerConfig{GeodataUpdateEnabled: true, GeodataUpdateTime: "04:00"},
		Connectivity: ConnectivityConfig{URL: "https://www.google.com/generate_204", ExpectedStatus: 204, TimeoutSeconds: 15},
		Web:          WebConfig{ExporterURL: "http://127.0.0.1:9101", Host: "127.0.0.1", Port: 3000, ControlEnabled: true},
		Backup:       BackupConfig{RetainPerTemplate: 10},
		Xray: XrayConfig{
			Binary:   "/usr/local/bin/xray",
			API:      "127.0.0.1:10085",
			AssetDir: "/usr/local/share/xray",
		},
		Geodata: GeodataConfig{
			MaxAssetSizeMB: geodatalimits.DefaultAssetSizeMB,
			Builder:        "/usr/local/bin/domain-list-community",
			UserGeosite:    DefaultUserGeositeName,
			UserGeoip:      DefaultUserGeoipName,
			Geosite:        defaultGeodataSource(),
			Geoip:          defaultGeodataSource(),
		},
		Storage: StorageConfig{Database: "/var/lib/xray-core-helper/helper.sqlite"},
		Server: ServerConfig{
			Socket:      "/run/xray-core-helper/helper.sock",
			SocketGroup: "xray-core-helper",
		},
	}
}

func Load(path string) (Config, error) {
	config := Defaults()
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open helper config: %w", err)
	}
	defer file.Close()

	decoder := toml.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode helper config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func defaultGeodataSource() GeodataSourceConfig {
	return GeodataSourceConfig{
		Checksum: GeodataChecksumConfig{Algorithm: sha256Algorithm},
	}
}

func (config Config) Validate() error {
	if err := config.Network.Validate(); err != nil {
		return err
	}
	if err := config.Logging.Validate(); err != nil {
		return err
	}
	if err := config.Scheduler.Validate(); err != nil {
		return err
	}
	if err := config.Connectivity.Validate(); err != nil {
		return err
	}
	if err := config.Web.Validate(); err != nil {
		return err
	}
	if config.Backup.RetainPerTemplate < 1 || config.Backup.RetainPerTemplate > 100 {
		return fmt.Errorf("backup.retain_per_template must be between 1 and 100")
	}
	paths := map[string]string{
		"xray.binary":      config.Xray.Binary,
		"xray.asset_dir":   config.Xray.AssetDir,
		"storage.database": config.Storage.Database,
		"server.socket":    config.Server.Socket,
	}
	for name, value := range paths {
		if !filepath.IsAbs(value) {
			return fmt.Errorf("%s must be an absolute path", name)
		}
	}
	if !filepath.IsAbs(config.Geodata.Builder) || filepath.Clean(config.Geodata.Builder) != config.Geodata.Builder {
		return fmt.Errorf("geodata.builder must be a clean absolute path")
	}
	if !geodatalimits.ValidMB(config.Geodata.MaxAssetSizeMB) {
		return fmt.Errorf("geodata.max_asset_size_mb must be a positive size in MB within the supported integer range")
	}
	if !ValidUserGeositeName(config.Geodata.UserGeosite) {
		return fmt.Errorf("geodata.user_geosite must be a safe geosite_*.dat basename")
	}
	if !ValidUserGeoipName(config.Geodata.UserGeoip) {
		return fmt.Errorf("geodata.user_geoip must be a safe geoip_*.dat basename")
	}
	if config.Xray.API == "" {
		return fmt.Errorf("xray.api must not be empty")
	}
	if !systemGroupName.MatchString(config.Server.SocketGroup) {
		return fmt.Errorf("server.socket_group must be a valid system group name")
	}
	for _, assetName := range []string{geositeAssetName, geoipAssetName} {
		if _, err := config.GeodataAssetPath(assetName); err != nil {
			return err
		}
	}
	if err := config.Geodata.Validate(); err != nil {
		return err
	}
	return nil
}

func (config WebConfig) Validate() error {
	if config.ExporterURL != "" {
		u, err := url.Parse(config.ExporterURL)
		if err != nil || u == nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
			return fmt.Errorf("web.exporter_url must be an HTTP loopback origin")
		}
		if u.Port() != "" {
			p, err := strconv.Atoi(u.Port())
			if err != nil || p < 1 || p > 65535 {
				return fmt.Errorf("web.exporter_url has invalid port")
			}
		}
	}

	ip := net.ParseIP(config.Host)
	if ip == nil || (!ip.IsGlobalUnicast() && !ip.IsLoopback()) || ip.IsLinkLocalUnicast() {
		return fmt.Errorf("web.host must be a specific unicast IP")
	}
	if config.Port < 1 || config.Port > 65535 {
		return fmt.Errorf("web.port must be between 1 and 65535")
	}
	return nil
}

// GeodataAssetPath returns the fixed managed asset path without accessing the
// filesystem.
func (config Config) GeodataAssetPath(name string) (string, error) {
	if name != geositeAssetName && name != geoipAssetName {
		return "", fmt.Errorf("unsupported geodata asset name")
	}
	switch config.Xray.AssetDir {
	case "/usr/local/share/xray", "/var/lib/xray-core-helper/geodata":
		return filepath.Join(config.Xray.AssetDir, name), nil
	default:
		return "", fmt.Errorf("xray.asset_dir must be an allowed Xray or helper geodata directory")
	}
}

func (config GeodataConfig) Validate() error {
	origins := make(map[string]struct{}, len(config.Origins))
	for _, origin := range config.Origins {
		parsed, err := parseHTTPSURL(origin, false)
		if err != nil || parsed.Path != "" {
			return fmt.Errorf("geodata.origins must contain HTTPS origins without paths, credentials, queries, or fragments")
		}
		canonical := canonicalOrigin(parsed)
		if _, exists := origins[canonical]; exists {
			return fmt.Errorf("geodata.origins must not contain duplicates")
		}
		origins[canonical] = struct{}{}
	}

	sources := []struct {
		name   string
		source GeodataSourceConfig
	}{
		{name: "geodata.geosite", source: config.Geosite},
		{name: "geodata.geoip", source: config.Geoip},
	}
	anySourceConfigured := len(config.Origins) > 0
	for _, item := range sources {
		if item.source.URL != "" || item.source.Checksum.URL != "" {
			anySourceConfigured = true
		}
		if item.source.Checksum.Algorithm != sha256Algorithm {
			return fmt.Errorf("%s.checksum.algorithm must be sha256", item.name)
		}
	}
	if !anySourceConfigured {
		return nil
	}
	if len(config.Origins) == 0 {
		return fmt.Errorf("geodata.origins is required when remote sources are configured")
	}
	for _, item := range sources {
		if item.source.URL == "" || item.source.Checksum.URL == "" {
			return fmt.Errorf("%s.url and %s.checksum.url are both required for geosite and geoip", item.name, item.name)
		}
		if err := config.validateRemoteURL(item.source.URL); err != nil {
			return fmt.Errorf("%s.url must use an explicitly allowed HTTPS origin without credentials, queries, or fragments", item.name)
		}
		if err := config.validateRemoteURL(item.source.Checksum.URL); err != nil {
			return fmt.Errorf("%s.checksum.url must use an explicitly allowed HTTPS origin without credentials, queries, or fragments", item.name)
		}
	}
	return nil
}

// AllowsRemoteURL reports whether a remote request or redirect target uses an
// explicitly configured HTTPS origin and has no credentials, query, or fragment.
func (config GeodataConfig) AllowsRemoteURL(raw string) bool {
	return config.validateRemoteURL(raw) == nil
}

func (config GeodataConfig) validateRemoteURL(raw string) error {
	parsed, err := parseHTTPSURL(raw, true)
	if err != nil || parsed.Path == "" {
		return fmt.Errorf("invalid remote URL")
	}
	for _, origin := range config.Origins {
		allowed, err := parseHTTPSURL(origin, false)
		if err == nil && allowed.Path == "" && canonicalOrigin(allowed) == canonicalOrigin(parsed) {
			return nil
		}
	}
	return fmt.Errorf("remote URL origin is not configured")
}

func parseHTTPSURL(raw string, allowPath bool) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil ||
		parsed.Scheme != "https" ||
		parsed.Opaque != "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.ForceQuery ||
		strings.Contains(raw, "#") ||
		parsed.Fragment != "" ||
		parsed.Host == "" {
		return nil, fmt.Errorf("invalid HTTPS URL")
	}
	if !allowPath && parsed.Path != "" {
		return nil, fmt.Errorf("HTTPS origin must not include a path")
	}
	host := parsed.Hostname()
	if !validRemoteHost(host) {
		return nil, fmt.Errorf("invalid HTTPS host")
	}
	port := parsed.Port()
	if strings.HasSuffix(parsed.Host, ":") {
		return nil, fmt.Errorf("invalid HTTPS port")
	}
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, fmt.Errorf("invalid HTTPS port")
		}
	}
	return parsed, nil
}

func validRemoteHost(host string) bool {
	if net.ParseIP(host) != nil {
		return !strings.Contains(host, "%")
	}
	if host == "" || len(host) > 253 || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') &&
				character != '-' {
				return false
			}
		}
	}
	return true
}

func canonicalOrigin(parsed *url.URL) string {
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" && port != "443" {
		host += ":" + port
	}
	return "https://" + host
}

// ValidUserGeositeName restricts the managed file to one non-reserved basename.
func ValidUserGeositeName(name string) bool {
	return userGeositeName.MatchString(name)
}

func ValidUserGeoipName(name string) bool { return userGeoipName.MatchString(name) }
