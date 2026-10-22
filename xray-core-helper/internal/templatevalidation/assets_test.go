package templatevalidation

import (
	"context"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/transaction"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeXrayValidatorPassesExplicitAssetEnvironment(t *testing.T) {
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	binary := filepath.Join(root, "xray-fixture")
	candidate := filepath.Join(root, "candidate.json")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' \"$XRAY_LOCATION_ASSET\" > \"$4.environment\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewWithAssets(nil, assets)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := transaction.NewXrayValidator(binary, assets)
	if err != nil {
		t.Fatal(err)
	}
	registry.runner.(*boundedRunners).xray = validator
	if _, err = registry.Validate(context.Background(), templatecatalog.XrayConfig, candidate); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(candidate + ".environment")
	if err != nil || string(raw) != assets {
		t.Fatal("asset environment lost", err)
	}
}
