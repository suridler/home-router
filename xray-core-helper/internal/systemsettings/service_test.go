package systemsettings

import (
	"context"
	"home-router/xray-core-helper/internal/testconfig"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/api"
)

func TestPutPersistsAndUpdatesRuntimeValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[network]\ninterface = 'br-lan'\naddress = '10.0.0.1'\nipv4_prefix = '10.0.0.0/24'\nintercept_mark = 2\npolicy_table = 321\n[scheduler]\ngeodata_update_enabled = false\ngeodata_update_time = '23:45'\n[backup]\nretain_per_template = 10\n[web]\nhost = '192.0.2.1'\nport = 3001\ncontrol_enabled = false\n"), 0640); err != nil {
		t.Fatal(err)
	}
	config, err := testconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(path, config)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Put(context.Background(), api.UpdateSystemSettingsRequest{Backup: api.BackupSettings{RetainPerTemplate: 25}})
	if err != nil || updated.Backup.RetainPerTemplate != 25 {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
	loaded, err := testconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Backup.RetainPerTemplate != 25 || service.Get(context.Background()).Backup.RetainPerTemplate != 25 {
		t.Fatal("persisted and runtime values differ")
	}
	if loaded.Web != config.Web {
		t.Fatal("backup retention update changed helper-owned web policy")
	}
	if loaded.Network != config.Network {
		t.Fatal("settings update changed selected network")
	}
	if loaded.Scheduler != config.Scheduler {
		t.Fatal("settings update changed maintenance schedule")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}

func TestPutRejectsLimitsWithoutChangingFile(t *testing.T) {
	for _, value := range []int{0, 101} {
		t.Run(string(rune(value)), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			original := []byte("[backup]\nretain_per_template = 10\n")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			config, _ := testconfig.Load(path)
			service, _ := New(path, config)
			if _, err := service.Put(context.Background(), api.UpdateSystemSettingsRequest{Backup: api.BackupSettings{RetainPerTemplate: value}}); err != ErrInvalidSettings {
				t.Fatalf("err=%v", err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != string(original) {
				t.Fatal("invalid update changed file")
			}
		})
	}
}

func TestConnectivitySettingsApplyImmediatelyAndSurviveReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[backup]\nretain_per_template = 10\n"), 0600)
	config, _ := testconfig.Load(path)
	service, _ := New(path, config)
	want := api.ConnectivitySettings{URL: "https://example.com/check", ExpectedStatus: 200, TimeoutSeconds: 10}
	if _, err := service.Put(context.Background(), api.UpdateSystemSettingsRequest{Backup: api.BackupSettings{RetainPerTemplate: 10}, Connectivity: &want}); err != nil {
		t.Fatal(err)
	}
	loaded, err := testconfig.Load(path)
	if err != nil || loaded.Connectivity.URL != want.URL || service.Connectivity() != loaded.Connectivity {
		t.Fatal(loaded.Connectivity, err)
	}
	if _, err := service.Put(context.Background(), api.UpdateSystemSettingsRequest{Backup: api.BackupSettings{RetainPerTemplate: 20}}); err != nil || service.Get(context.Background()).Connectivity != want {
		t.Fatal("retention update changed connectivity", err)
	}
}
