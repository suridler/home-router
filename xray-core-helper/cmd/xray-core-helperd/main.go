package main

import (
	"flag"
	"log"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/helperapp"
	"home-router/xray-core-helper/internal/nativewrite"
)

var version = "dev"

func main() {
	commitNftables := flag.Bool("commit-nftables", false, "execute fixed systemd nftables file commit")
	configPath := flag.String("config", bootstrap.DefaultConfigPath, "path to helper TOML config")
	flag.Parse()
	if *commitNftables {
		if err := nativewrite.Execute(); err != nil {
			log.Fatal("native nftables file commit failed")
		}
		return
	}
	if err := helperapp.Run(*configPath, version); err != nil {
		log.Fatal(err)
	}
}
