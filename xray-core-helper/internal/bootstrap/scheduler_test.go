package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSchedulerConfigDefaultsAndValidation(t *testing.T) {
	for _, sample := range []struct {
		body, at       string
		enabled, valid bool
	}{
		{"", "04:00", true, true},
		{"[scheduler]\ngeodata_update_enabled = false\ngeodata_update_time = \"23:45\"", "23:45", false, true},
		{"[scheduler]\ngeodata_update_time = \"24:00\"", "", false, false},
		{"[scheduler]\ngeodata_update_time = \"4:00\"", "", false, false},
		{"[scheduler]\ngeodata_update_time = \"04:00:00\"", "", false, false},
	} {
		path := filepath.Join(t.TempDir(), "helper.toml")
		if err := os.WriteFile(path, []byte(sample.body), 0600); err != nil {
			t.Fatal(err)
		}
		config, err := testLoad(path)
		if (err == nil) != sample.valid {
			t.Fatalf("%q: %v", sample.body, err)
		}
		if sample.valid && (config.Scheduler.GeodataUpdateEnabled != sample.enabled || config.Scheduler.GeodataUpdateTime != sample.at) {
			t.Fatal(config.Scheduler)
		}
	}
}
