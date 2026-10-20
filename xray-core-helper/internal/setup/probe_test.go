package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProbeConfigStates(t *testing.T) {
	tests := []struct {
		name, body    string
		state         ConfigState
		setup, choice bool
	}{
		{"empty", "", ConfigEmpty, true, false},
		{"whitespace", " \n\t", ConfigWhitespace, true, false},
		{"empty object", " \n{}\n", ConfigEmptyObject, true, false},
		{"existing", `{"log":{}}`, ConfigExisting, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(test.body), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ProbeConfig(path)
			if err != nil || got.State != test.state || got.SetupRequired != test.setup || got.RequiresChoice != test.choice {
				t.Fatalf("got=%#v err=%v", got, err)
			}
		})
	}
	missing, err := ProbeConfig(filepath.Join(t.TempDir(), "missing"))
	if err != nil || missing.State != ConfigAbsent || !missing.SetupRequired {
		t.Fatalf("missing=%#v err=%v", missing, err)
	}
}
