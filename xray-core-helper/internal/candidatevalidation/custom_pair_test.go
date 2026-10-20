package candidatevalidation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func customAssetSHA(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func stageCustomAssetPair(t *testing.T, fixture *testFixture, geositeContent, geoipContent string) (string, string) {
	t.Helper()
	sourceDir := filepath.Join(fixture.companionDir, customAssetPendingDir)
	if err := os.MkdirAll(sourceDir, 0700); err != nil {
		t.Fatal(err)
	}
	geositeName := fixture.config.Geodata.UserGeosite
	geoipName := fixture.config.Geodata.UserGeoip
	writeAsset(t, filepath.Join(sourceDir, geositeName), geositeContent)
	writeAsset(t, filepath.Join(sourceDir, geoipName), geoipContent)
	return filepath.Join(fixture.config.Xray.AssetDir, customAssetPendingDir, geositeName),
		filepath.Join(fixture.config.Xray.AssetDir, customAssetPendingDir, geoipName)
}

func TestValidateCustomAssetsStagesBothSourcesAndCopiesCompanionFromLive(t *testing.T) {
	fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:USERLIST","ext:geosite_AA.dat:EXTRA"]}]}}`)
	const geositeContent = "staged geosite"
	const geoipContent = "staged geoip without a live file"
	geositeSource, geoipSource := stageCustomAssetPair(t, fixture, geositeContent, geoipContent)
	liveGeoip := filepath.Join(fixture.companionDir, fixture.config.Geodata.UserGeoip)
	if _, err := os.Stat(liveGeoip); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("live user geoip unexpectedly exists: %v", err)
	}
	writeAsset(t, filepath.Join(fixture.companionDir, "geosite_AA.dat"), "live companion")

	runner := &fakeRunner{onRun: func(_ context.Context, executable string, args, environment []string) error {
		if executable != fixture.config.Xray.Binary ||
			!reflect.DeepEqual(args, []string{"run", "-test", "-config", fixture.config.Xray.Config}) ||
			len(environment) != 1 || !strings.HasPrefix(environment[0], "XRAY_LOCATION_ASSET=") {
			t.Fatalf("Xray command = %#v, %#v, %#v", executable, args, environment)
		}
		candidateDir := strings.TrimPrefix(environment[0], "XRAY_LOCATION_ASSET=")
		if !filepath.IsAbs(candidateDir) || filepath.Dir(candidateDir) != fixture.root {
			t.Fatalf("candidate directory = %q", candidateDir)
		}
		info, err := os.Stat(candidateDir)
		if err != nil || info.Mode().Perm() != candidateDirectoryMode {
			t.Fatalf("candidate directory mode = %v, error = %v", info, err)
		}
		entries, err := os.ReadDir(candidateDir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if want := []string{"geoip_user.dat", "geosite_AA.dat", "geosite_user.dat"}; !reflect.DeepEqual(names, want) {
			t.Errorf("candidate files = %#v; want %#v", names, want)
		}
		for name, want := range map[string]string{
			"geosite_user.dat": geositeContent,
			"geoip_user.dat":   geoipContent,
			"geosite_AA.dat":   "live companion",
		} {
			content, err := os.ReadFile(filepath.Join(candidateDir, name))
			if err != nil || string(content) != want {
				t.Errorf("candidate %s = %q, %v; want %q", name, content, err, want)
			}
			info, err := os.Stat(filepath.Join(candidateDir, name))
			if err != nil || info.Mode().Perm() != candidateAssetMode {
				t.Errorf("candidate %s mode = %v, error = %v", name, info, err)
			}
		}
		return nil
	}}

	err := fixture.validator(t, runner).ValidateCustomAssets(
		context.Background(),
		geositeSource, customAssetSHA(geositeContent),
		geoipSource, customAssetSHA(geoipContent),
	)
	if err != nil {
		t.Fatalf("ValidateCustomAssets() error = %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("Xray ran %d times; want 1", runner.calls)
	}
	if len(fixture.files.removed) != 1 {
		t.Fatalf("candidate cleanup calls = %d", len(fixture.files.removed))
	}
	if _, err := os.Stat(fixture.files.removed[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate remains after success: %v", err)
	}
	if _, err := os.Stat(liveGeoip); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("live user geoip was created: %v", err)
	}
	liveCompanion, err := os.ReadFile(filepath.Join(fixture.companionDir, "geosite_AA.dat"))
	if err != nil || string(liveCompanion) != "live companion" {
		t.Fatalf("live companion = %q, %v", liveCompanion, err)
	}
}

func TestValidateCustomAssetsEnforcesBothHashesBeforeRunner(t *testing.T) {
	tests := []struct {
		name        string
		geositeHash string
		geoipHash   string
	}{
		{name: "wrong geosite hash", geositeHash: customAssetSHA("different geosite")},
		{name: "wrong geoip hash", geoipHash: customAssetSHA("different geoip")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:USERLIST"]}]}}`)
			const geositeContent = "staged geosite"
			const geoipContent = "staged geoip"
			geositeSource, geoipSource := stageCustomAssetPair(t, fixture, geositeContent, geoipContent)
			if test.geositeHash == "" {
				test.geositeHash = customAssetSHA(geositeContent)
			}
			if test.geoipHash == "" {
				test.geoipHash = customAssetSHA(geoipContent)
			}
			runner := &fakeRunner{}
			err := fixture.validator(t, runner).ValidateCustomAssets(
				context.Background(),
				geositeSource, test.geositeHash,
				geoipSource, test.geoipHash,
			)
			if !errors.Is(err, ErrCandidateChanged) {
				t.Fatalf("ValidateCustomAssets() error = %v; want candidate-change error", err)
			}
			if runner.called {
				t.Fatal("Xray ran before both hashes were verified")
			}
			if len(fixture.files.removed) != 1 {
				t.Fatalf("candidate cleanup calls = %d", len(fixture.files.removed))
			}
			if _, statErr := os.Stat(fixture.files.removed[0]); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("candidate remains after hash mismatch: %v", statErr)
			}
		})
	}
}

func TestValidateCustomAssetsRejectsInvalidInput(t *testing.T) {
	t.Run("wrong geosite path", func(t *testing.T) {
		fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:USERLIST"]}]}}`)
		const geositeContent = "staged geosite"
		const geoipContent = "staged geoip"
		_, geoipSource := stageCustomAssetPair(t, fixture, geositeContent, geoipContent)
		wrongGeosite := filepath.Join(fixture.config.Xray.AssetDir, customAssetPendingDir, "geosite_other.dat")
		assertCustomAssetsInvalidInput(t, fixture, wrongGeosite, customAssetSHA(geositeContent), geoipSource, customAssetSHA(geoipContent))
	})

	t.Run("wrong geoip path", func(t *testing.T) {
		fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:USERLIST"]}]}}`)
		const geositeContent = "staged geosite"
		const geoipContent = "staged geoip"
		geositeSource, _ := stageCustomAssetPair(t, fixture, geositeContent, geoipContent)
		wrongGeoip := filepath.Join(fixture.config.Xray.AssetDir, customAssetPendingDir, "geoip_other.dat")
		assertCustomAssetsInvalidInput(t, fixture, geositeSource, customAssetSHA(geositeContent), wrongGeoip, customAssetSHA(geoipContent))
	})

	t.Run("path for wrong configured name", func(t *testing.T) {
		fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_custom.dat:USERLIST"]}]}}`)
		fixture.config.Geodata.UserGeosite = "geosite_custom.dat"
		const geositeContent = "staged geosite"
		const geoipContent = "staged geoip"
		_, geoipSource := stageCustomAssetPair(t, fixture, geositeContent, geoipContent)
		defaultNamePath := filepath.Join(fixture.config.Xray.AssetDir, customAssetPendingDir, "geosite_user.dat")
		assertCustomAssetsInvalidInput(t, fixture, defaultNamePath, customAssetSHA(geositeContent), geoipSource, customAssetSHA(geoipContent))
	})

	t.Run("invalid geosite SHA", func(t *testing.T) {
		fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:USERLIST"]}]}}`)
		const geositeContent = "staged geosite"
		const geoipContent = "staged geoip"
		geositeSource, geoipSource := stageCustomAssetPair(t, fixture, geositeContent, geoipContent)
		assertCustomAssetsInvalidInput(t, fixture, geositeSource, "not-a-sha256", geoipSource, customAssetSHA(geoipContent))
	})

	t.Run("invalid geoip SHA", func(t *testing.T) {
		fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:USERLIST"]}]}}`)
		const geositeContent = "staged geosite"
		const geoipContent = "staged geoip"
		geositeSource, geoipSource := stageCustomAssetPair(t, fixture, geositeContent, geoipContent)
		assertCustomAssetsInvalidInput(t, fixture, geositeSource, customAssetSHA(geositeContent), geoipSource, strings.Repeat("A", 64))
	})
}

func assertCustomAssetsInvalidInput(t *testing.T, fixture *testFixture, geositeSource, geositeHash, geoipSource, geoipHash string) {
	t.Helper()
	runner := &fakeRunner{}
	err := fixture.validator(t, runner).ValidateCustomAssets(context.Background(), geositeSource, geositeHash, geoipSource, geoipHash)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ValidateCustomAssets() error = %v; want invalid input", err)
	}
	if runner.called || len(fixture.files.removed) != 0 {
		t.Fatalf("runner called = %t, cleanup calls = %d", runner.called, len(fixture.files.removed))
	}
}

func TestValidateCustomAssetsCancellation(t *testing.T) {
	t.Run("pre-canceled", func(t *testing.T) {
		fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:USERLIST"]}]}}`)
		const geositeContent = "staged geosite"
		const geoipContent = "staged geoip"
		geositeSource, geoipSource := stageCustomAssetPair(t, fixture, geositeContent, geoipContent)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runner := &fakeRunner{}
		err := fixture.validator(t, runner).ValidateCustomAssets(ctx,
			geositeSource, customAssetSHA(geositeContent), geoipSource, customAssetSHA(geoipContent))
		if !errors.Is(err, ErrValidationCanceled) {
			t.Fatalf("ValidateCustomAssets() error = %v; want canceled validation", err)
		}
		if runner.called || len(fixture.files.removed) != 0 {
			t.Fatalf("runner called = %t, cleanup calls = %d", runner.called, len(fixture.files.removed))
		}
	})

	t.Run("during asset copy", func(t *testing.T) {
		fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:USERLIST"]}]}}`)
		const geositeContent = "staged geosite"
		const geoipContent = "staged geoip"
		geositeSource, geoipSource := stageCustomAssetPair(t, fixture, geositeContent, geoipContent)
		ctx, cancel := context.WithCancel(context.Background())
		fixture.files.onSource = func(path string) {
			if path == filepath.Join(fixture.companionDir, customAssetPendingDir, fixture.config.Geodata.UserGeosite) {
				cancel()
			}
		}
		runner := &fakeRunner{}
		err := fixture.validator(t, runner).ValidateCustomAssets(ctx,
			geositeSource, customAssetSHA(geositeContent), geoipSource, customAssetSHA(geoipContent))
		if !errors.Is(err, ErrValidationCanceled) {
			t.Fatalf("ValidateCustomAssets() error = %v; want canceled validation", err)
		}
		if runner.called {
			t.Fatal("Xray ran after cancellation during copy")
		}
		if len(fixture.files.removed) != 1 {
			t.Fatalf("candidate cleanup calls = %d", len(fixture.files.removed))
		}
		if _, statErr := os.Stat(fixture.files.removed[0]); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("candidate remains after cancellation: %v", statErr)
		}
	})
}

func TestValidateCustomAssetsMissingCompanion(t *testing.T) {
	fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:USERLIST","ext:geosite_ZZ.dat:MISSING"]}]}}`)
	const geositeContent = "staged geosite"
	const geoipContent = "staged geoip"
	geositeSource, geoipSource := stageCustomAssetPair(t, fixture, geositeContent, geoipContent)
	runner := &fakeRunner{}
	err := fixture.validator(t, runner).ValidateCustomAssets(context.Background(),
		geositeSource, customAssetSHA(geositeContent), geoipSource, customAssetSHA(geoipContent))
	if !errors.Is(err, ErrMissingCompanionAsset) {
		t.Fatalf("ValidateCustomAssets() error = %v; want missing companion error", err)
	}
	if runner.called {
		t.Fatal("Xray ran with a missing companion")
	}
	if len(fixture.files.removed) != 1 {
		t.Fatalf("candidate cleanup calls = %d", len(fixture.files.removed))
	}
}

func TestValidateCustomAssetsCleanupFailureIsRedacted(t *testing.T) {
	fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:USERLIST"]}]}}`)
	const geositeContent = "staged geosite"
	const geoipContent = "staged geoip"
	geositeSource, geoipSource := stageCustomAssetPair(t, fixture, geositeContent, geoipContent)
	fixture.files.removeErr = errors.New("remove /secret/candidate")
	err := fixture.validator(t, &fakeRunner{}).ValidateCustomAssets(context.Background(),
		geositeSource, customAssetSHA(geositeContent), geoipSource, customAssetSHA(geoipContent))
	if !errors.Is(err, ErrCandidateCleanup) || strings.Contains(err.Error(), "/secret") {
		t.Fatalf("ValidateCustomAssets() error = %v; want redacted cleanup error", err)
	}
}

func TestValidateCustomAssetsUsesConfiguredUserGeoipOverride(t *testing.T) {
	fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_custom.dat:USERLIST"]},{"ip":["ext:geoip_custom.dat:USERLIST"]}]}}`)
	fixture.config.Geodata.UserGeosite = "geosite_custom.dat"
	fixture.config.Geodata.UserGeoip = "geoip_custom.dat"
	const geositeContent = "new custom geosite"
	const geoipContent = "new custom geoip"
	geositeSource, geoipSource := stageCustomAssetPair(t, fixture, geositeContent, geoipContent)
	writeAsset(t, filepath.Join(fixture.companionDir, "geosite_custom.dat"), "old live geosite")
	writeAsset(t, filepath.Join(fixture.companionDir, "geoip_custom.dat"), "old live geoip")

	runner := &fakeRunner{onRun: func(_ context.Context, _ string, _, environment []string) error {
		candidateDir := strings.TrimPrefix(environment[0], "XRAY_LOCATION_ASSET=")
		entries, err := os.ReadDir(candidateDir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if want := []string{"geoip_custom.dat", "geosite_custom.dat"}; !reflect.DeepEqual(names, want) {
			t.Errorf("candidate files = %#v; want %#v", names, want)
		}
		for name, want := range map[string]string{
			"geosite_custom.dat": geositeContent,
			"geoip_custom.dat":   geoipContent,
		} {
			content, err := os.ReadFile(filepath.Join(candidateDir, name))
			if err != nil || string(content) != want {
				t.Errorf("candidate %s = %q, %v; want %q", name, content, err, want)
			}
		}
		return nil
	}}

	err := fixture.validator(t, runner).ValidateCustomAssets(context.Background(),
		geositeSource, customAssetSHA(geositeContent), geoipSource, customAssetSHA(geoipContent))
	if err != nil {
		t.Fatalf("ValidateCustomAssets() error = %v", err)
	}
	for name, want := range map[string]string{
		"geosite_custom.dat": "old live geosite",
		"geoip_custom.dat":   "old live geoip",
	} {
		content, err := os.ReadFile(filepath.Join(fixture.companionDir, name))
		if err != nil || string(content) != want {
			t.Fatalf("live %s = %q, %v; want %q", name, content, err, want)
		}
	}
}

func TestValidateCustomAssetsRequiresReferencedCustomGeosite(t *testing.T) {
	fixture := newFixture(t, `{"routing":{"rules":[{"ip":["ext:geoip_user.dat:USERLIST"]}]}}`)
	const geositeContent = "staged geosite"
	const geoipContent = "staged geoip"
	geositeSource, geoipSource := stageCustomAssetPair(t, fixture, geositeContent, geoipContent)
	runner := &fakeRunner{}
	err := fixture.validator(t, runner).ValidateCustomAssets(context.Background(),
		geositeSource, customAssetSHA(geositeContent), geoipSource, customAssetSHA(geoipContent))
	if !errors.Is(err, ErrUnsupportedAsset) {
		t.Fatalf("ValidateCustomAssets() error = %v; want unsupported asset error", err)
	}
	if runner.called || len(fixture.files.removed) != 0 {
		t.Fatalf("runner called = %t, cleanup calls = %d", runner.called, len(fixture.files.removed))
	}
}
