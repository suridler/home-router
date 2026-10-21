package xrayservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallationUsesServiceBinaryConfigAndAssets(t *testing.T) {
	properties := map[string]any{
		"ExecStart":        [][]any{{"/opt/xray/xray", []string{"/opt/xray/xray", "run", "-config", "/etc/custom-xray/main.json"}, false, 0, 0, 0, 0, 0, 0, 0}},
		"Environment":      []string{"UNRELATED=private", "XRAY_LOCATION_ASSET=/var/lib/xray-core-helper/geodata"},
		"EnvironmentFiles": [][]any{}, "PassEnvironment": []string{}, "UnsetEnvironment": []string{},
	}
	r := &Resolver{run: func(_ context.Context, args []string) ([]byte, error) {
		name := args[len(args)-1]
		kind, value := "s", any("")
		if name == "xray.service" {
			kind, value = "o", []string{"/org/freedesktop/systemd1/unit/xray_2eservice"}
		} else if v, ok := properties[name]; ok {
			value = v
			switch name {
			case "ExecStart":
				kind = "a(sasbttttuii)"
			case "EnvironmentFiles":
				kind = "a(sb)"
			default:
				kind = "as"
			}
		}
		return json.Marshal(map[string]any{"type": kind, "data": value})
	}}
	i, err := r.Resolve(context.Background())
	if err != nil || i.Binary != "/opt/xray/xray" || i.Config != "/etc/custom-xray/main.json" || i.AssetDir != "/var/lib/xray-core-helper/geodata" {
		t.Fatal(i, err)
	}
	properties["UnsetEnvironment"] = []string{"XRAY_LOCATION_ASSET"}
	if _, err = r.Resolve(context.Background()); err == nil {
		t.Fatal("ignored unset")
	}
	properties["UnsetEnvironment"] = []string{}
	properties["Environment"] = []string{}
	if _, err = r.Resolve(context.Background()); err == nil {
		t.Fatal("guessed missing assets")
	}
}

func TestEnvironmentAssetSubset(t *testing.T) {
	for _, text := range []string{"XRAY_LOCATION_ASSET=/usr/local/share/xray", "# comment\nXRAY_LOCATION_ASSET='/etc/xray/assets'", "XRAY_LOCATION_ASSET=\"/etc/xray/assets dir\""} {
		if _, found, err := assetAssignment(text); err != nil || !found {
			t.Fatal(err)
		}
	}
	for _, text := range []string{"OTHER=\"unfinished\nXRAY_LOCATION_ASSET=/wrong", "XRAY_LOCATION_ASSET=$ASSETS", "XRAY_LOCATION_ASSET=relative", "XRAY_LOCATION_ASSET='/unterminated", "XRAY_LOCATION_ASSET=/one\\\n/two"} {
		if _, _, err := assetAssignment(text); err == nil {
			t.Fatal("accepted unsupported form")
		}
	}
}

func TestPathDriftBlocksMutationsButRetainsReadAccess(t *testing.T) {
	i := Installation{Unit: "xray.service", Config: "/etc/xray/a.json"}
	called := 0
	h := Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(204) }), i, func(context.Context) (Installation, error) { return Installation{Config: "/etc/xray/b.json"}, nil }, nil)
	for _, test := range []struct {
		method string
		want   int
	}{{"GET", 204}, {"POST", 409}, {"PATCH", 409}} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(test.method, "/v1/setup/adopt", nil))
		if response.Code != test.want {
			t.Fatal(response.Code)
		}
	}
	if called != 1 {
		t.Fatal("mutation reached handler")
	}
}

// adminManagedDirectory models an administrator-owned path as seen by the
// unprivileged test process: the temporary tree is owned by the test user and
// widened to group/world-writable so no root ownership or safe mode is implied.
func adminManagedDirectory(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	admin := filepath.Join(root, "admin")
	if err := os.Mkdir(admin, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, admin} {
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	return root, admin
}

func writeTestFile(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func installationResolver(properties map[string]any) *Resolver {
	return &Resolver{run: func(_ context.Context, args []string) ([]byte, error) {
		name := args[len(args)-1]
		kind, value := "s", any("")
		if name == "xray.service" {
			kind, value = "o", []string{"/org/freedesktop/systemd1/unit/xray_2eservice"}
		} else if v, ok := properties[name]; ok {
			value = v
			switch name {
			case "ExecStart":
				kind = "a(sasbttttuii)"
			case "EnvironmentFiles":
				kind = "a(sb)"
			default:
				kind = "as"
			}
		}
		return json.Marshal(map[string]any{"type": kind, "data": value})
	}}
}

func TestTrustedPathIgnoresAdministratorManagedOwnershipAndMode(t *testing.T) {
	root, admin := adminManagedDirectory(t)
	assets := filepath.Join(admin, "assets")
	if err := os.Mkdir(assets, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(assets, 0o777); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(admin, "xray.env")
	writeTestFile(t, envFile, []byte("XRAY_LOCATION_ASSET="+assets+"\n"), 0o664)
	binary := filepath.Join(admin, "xray")
	writeTestFile(t, binary, []byte("#!/bin/sh\n"), 0o775)
	missingConfig := filepath.Join(admin, "config.json")

	for _, path := range []string{envFile, assets} {
		if err := TrustedPath(path, false); err != nil {
			t.Fatalf("administrator-managed path %s rejected: %v", path, err)
		}
	}
	if err := TrustedPath(binary, true); err != nil {
		t.Fatalf("group-writable executable rejected: %v", err)
	}
	if err := TrustedPath(missingConfig, false); err != nil {
		t.Fatalf("missing config leaf rejected for setup: %v", err)
	}
	installation := Installation{Binary: binary, Config: missingConfig, AssetDir: assets}
	if err := installation.CheckTrusted(); err != nil {
		t.Fatalf("complete installation with administrator-managed paths rejected: %v", err)
	}

	if err := TrustedPath("relative/xray.env", false); err == nil {
		t.Fatal("relative path accepted")
	}
	if err := TrustedPath(admin+"/../admin/xray.env", false); err == nil {
		t.Fatal("non-canonical path accepted")
	}
	leafLink := filepath.Join(admin, "xray-link.env")
	if err := os.Symlink(envFile, leafLink); err != nil {
		t.Fatal(err)
	}
	if err := TrustedPath(leafLink, false); err == nil {
		t.Fatal("symlinked leaf accepted")
	}
	parentLink := filepath.Join(root, "admin-link")
	if err := os.Symlink(admin, parentLink); err != nil {
		t.Fatal(err)
	}
	if err := TrustedPath(filepath.Join(parentLink, "xray.env"), false); err == nil {
		t.Fatal("symlinked parent accepted")
	}
	nonExecutable := filepath.Join(admin, "not-executable")
	writeTestFile(t, nonExecutable, []byte("text\n"), 0o644)
	if err := TrustedPath(nonExecutable, true); err == nil {
		t.Fatal("non-executable binary accepted")
	}
	if err := TrustedPath(admin, true); err == nil {
		t.Fatal("directory accepted as executable")
	}

	for name, invalid := range map[string]Installation{
		"config directory": {Binary: binary, Config: admin, AssetDir: assets},
		"asset file":       {Binary: binary, Config: missingConfig, AssetDir: envFile},
		"missing assets":   {Binary: binary, Config: missingConfig, AssetDir: filepath.Join(admin, "missing-assets")},
	} {
		if err := invalid.CheckTrusted(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestResolveAcceptsAdministratorManagedEnvironmentFile(t *testing.T) {
	_, admin := adminManagedDirectory(t)
	assets := filepath.Join(admin, "assets")
	if err := os.Mkdir(assets, 0o777); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(admin, "xray.env")
	writeTestFile(t, envFile, []byte("XRAY_LOCATION_ASSET="+assets+"\n"), 0o664)
	config := filepath.Join(admin, "config.json")
	properties := map[string]any{
		"ExecStart":        [][]any{{"/opt/xray/xray", []string{"/opt/xray/xray", "run", "-config", config}, false, 0, 0, 0, 0, 0, 0, 0}},
		"Environment":      []string{},
		"EnvironmentFiles": [][]any{{envFile, false}},
		"PassEnvironment":  []string{},
		"UnsetEnvironment": []string{},
	}
	info, err := os.Stat(envFile)
	if err != nil || info.Mode().Perm() != 0o664 {
		t.Fatalf("environment fixture mode = %v, %v", info, err)
	}
	installation, err := installationResolver(properties).Resolve(context.Background())
	if err != nil || installation.AssetDir != assets {
		t.Fatalf("administrator-managed environment file rejected: %v, %v", installation, err)
	}
}

func TestResolveEnvironmentFileFailuresStayErrors(t *testing.T) {
	_, admin := adminManagedDirectory(t)
	assets := filepath.Join(admin, "assets")
	if err := os.Mkdir(assets, 0o777); err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(admin, "valid.env")
	writeTestFile(t, valid, []byte("XRAY_LOCATION_ASSET="+assets+"\n"), 0o644)
	directory := filepath.Join(admin, "env-dir")
	if err := os.Mkdir(directory, 0o777); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(admin, "oversized.env")
	writeTestFile(t, oversized, make([]byte, (64<<10)+1), 0o644)
	symlink := filepath.Join(admin, "linked.env")
	if err := os.Symlink(valid, symlink); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
	}{
		{"missing required", filepath.Join(admin, "missing.env")},
		{"directory", directory},
		{"oversized", oversized},
		{"symlink", symlink},
	}
	if os.Geteuid() != 0 {
		unreadable := filepath.Join(admin, "unreadable.env")
		writeTestFile(t, unreadable, []byte("XRAY_LOCATION_ASSET="+assets+"\n"), 0o644)
		if err := os.Chmod(unreadable, 0o000); err != nil {
			t.Fatal(err)
		}
		tests = append(tests, struct{ name, path string }{"unreadable", unreadable})
	}
	config := filepath.Join(admin, "config.json")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			properties := map[string]any{
				"ExecStart":        [][]any{{"/opt/xray/xray", []string{"/opt/xray/xray", "run", "-config", config}, false, 0, 0, 0, 0, 0, 0, 0}},
				"Environment":      []string{},
				"EnvironmentFiles": [][]any{{test.path, false}},
				"PassEnvironment":  []string{},
				"UnsetEnvironment": []string{},
			}
			if _, err := installationResolver(properties).Resolve(context.Background()); err == nil {
				t.Fatalf("%s environment file accepted", test.name)
			}
		})
	}
}

func TestPathDiscoveryDoesNotGateIndependentMaintenance(t *testing.T) {
	resolutions := 0
	handled := 0
	handler := Guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { handled++; w.WriteHeader(204) }), Installation{}, func(context.Context) (Installation, error) {
		resolutions++
		return Installation{}, errors.New("service unavailable")
	}, nil)
	for _, path := range []string{"/v1/errors/collect", "/v1/network/probe", "/v1/templates/refresh"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", path, nil))
		if response.Code != 204 {
			t.Fatalf("%s: HTTP %d", path, response.Code)
		}
	}
	if resolutions != 0 || handled != 3 {
		t.Fatalf("resolutions=%d handled=%d", resolutions, handled)
	}
	for _, path := range []string{"/v1/xray/config/adopt", "/v1/proxy-domains/reconcile", "/v1/safe-apply/tx/rollback"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", path, nil))
		if response.Code != http.StatusConflict {
			t.Fatalf("%s: HTTP %d", path, response.Code)
		}
	}
	if handled != 3 {
		t.Fatal("dependent operation reached handler")
	}
}
