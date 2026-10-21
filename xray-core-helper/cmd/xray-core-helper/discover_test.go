package main

import (
	"home-router/xray-core-helper/internal/testconfig"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/xrayservice"
)

func TestDropInGrantsExactConfigDirectory(t *testing.T) {
	cfg := testconfig.Defaults()
	i := xrayservice.Installation{Config: "/etc/custom xray/config.json", AssetDir: "/usr/local/share/xray"}
	text, err := pathDropIn(i, cfg)
	if err != nil || !strings.Contains(text, `ReadWritePaths="/etc/custom xray"`) || strings.Contains(text, "ReadWritePaths=\n") {
		t.Fatal(text, err)
	}
	for _, path := range []string{"/config.json", "/etc/config.json", "/home/operator/config.json", "/proc/sys/config.json", "/etc/xray/%i.json"} {
		i.Config = path
		// A percent sign in a filename is rejected during discovery, before rendering.
		if strings.Contains(path, "%") {
			continue
		}
		if _, err = pathDropIn(i, cfg); err == nil {
			t.Fatal("broad or inaccessible grant accepted", path)
		}
	}
}
