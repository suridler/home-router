package bootstrap

func Defaults() Config {
	c, err := ReadDefaults("../../templConf/config.toml")
	if err != nil {
		panic(err)
	}
	return c
}

func testLoad(path string) (Config, error) {
	return LoadWithTemplate(path, "../../templConf/config.toml")
}
func testDecode(raw []byte) (Config, error) { return Decode(raw, Defaults()) }

func defaultGeodataSource() GeodataSourceConfig {
	return GeodataSourceConfig{Checksum: GeodataChecksumConfig{Algorithm: "sha256"}}
}

func init() { TemplateFile = "../../templConf/config.toml" }
