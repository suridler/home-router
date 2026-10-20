package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupRetentionDefaultAndLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[server]\nsocket = \"/tmp/helper.sock\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Backup.RetainPerTemplate != 10 {
		t.Fatalf("default=%d", config.Backup.RetainPerTemplate)
	}
	for _, value := range []int{1, 100} {
		config.Backup.RetainPerTemplate = value
		if err := config.Validate(); err != nil {
			t.Errorf("value %d: %v", value, err)
		}
	}
	for _, value := range []int{0, 101, -1} {
		config.Backup.RetainPerTemplate = value
		if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "backup.retain_per_template") {
			t.Errorf("value %d err=%v", value, err)
		}
	}
}
