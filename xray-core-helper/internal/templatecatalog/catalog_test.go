package templatecatalog

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultCatalog(t *testing.T) {
	catalog := Default()
	want := []Entry{
		{ID: XrayConfig, WorkingPath: WorkingRoot + "/xray/config.json", SystemPath: "/usr/local/etc/xray/config.json"},
		{ID: Nftables, WorkingPath: WorkingRoot + "/nftables/nftables.conf", SystemPath: "/etc/nftables.conf"},
		{ID: TProxyUnit, WorkingPath: WorkingRoot + "/systemd/xray-tproxy-routing.service", SystemPath: "/etc/systemd/system/xray-tproxy-routing.service"},
		{ID: Sysctl, WorkingPath: WorkingRoot + "/sysctl/99-xray.conf", SystemPath: "/etc/sysctl.d/99-xray.conf"},
	}
	if got := catalog.Entries(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Entries() = %#v; want %#v", got, want)
	}
	for _, wantEntry := range want {
		got, err := catalog.Lookup(wantEntry.ID)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", wantEntry.ID, err)
		}
		if got != wantEntry {
			t.Fatalf("Lookup(%q) = %#v; want %#v", wantEntry.ID, got, wantEntry)
		}
	}
}

func TestCatalogRejectsInvalidMappings(t *testing.T) {
	tests := []struct {
		name    string
		entries []Entry
		message string
	}{
		{name: "duplicate ID", entries: []Entry{allowed[0], allowed[0]}, message: "duplicate template ID"},
		{name: "unknown ID", entries: []Entry{{ID: "unknown", WorkingPath: "/etc/xray-core-helper/templConf/unknown", SystemPath: "/etc/unknown"}}, message: "unknown template"},
		{name: "relative working path", entries: []Entry{{ID: XrayConfig, WorkingPath: "templConf/xray/config.json", SystemPath: allowed[0].SystemPath}}, message: "working path must be absolute"},
		{name: "relative system path", entries: []Entry{{ID: XrayConfig, WorkingPath: allowed[0].WorkingPath, SystemPath: "usr/local/etc/xray/config.json"}}, message: "system path must be absolute"},
		{name: "working path alias", entries: []Entry{{ID: XrayConfig, WorkingPath: WorkingRoot + "/xray/../xray/config.json", SystemPath: allowed[0].SystemPath}}, message: "working path must be canonical"},
		{name: "system path alias", entries: []Entry{{ID: XrayConfig, WorkingPath: allowed[0].WorkingPath, SystemPath: "/usr/local/etc/xray/../xray/config.json"}}, message: "system path must be canonical"},
		{name: "allowlisted ID with other working path", entries: []Entry{{ID: XrayConfig, WorkingPath: WorkingRoot + "/xray/other.json", SystemPath: allowed[0].SystemPath}}, message: "outside the allowlist"},
		{name: "allowlisted ID with other system path", entries: []Entry{{ID: XrayConfig, WorkingPath: allowed[0].WorkingPath, SystemPath: "/tmp/config.json"}}, message: "outside the allowlist"},
		{name: "incomplete catalog", entries: allowed[:len(allowed)-1], message: "must contain all 4 allowlisted entries"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := newCatalog(test.entries)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("New() error = %v; want message containing %q", err, test.message)
			}
		})
	}
}

func TestCatalogLookupRejectsUnknownID(t *testing.T) {
	_, err := Default().Lookup("xray-config/../nftables")
	if !errors.Is(err, ErrUnknownTemplate) {
		t.Fatalf("Lookup() error = %v; want ErrUnknownTemplate", err)
	}
}

func TestEntriesReturnsCopy(t *testing.T) {
	catalog := Default()
	entries := catalog.Entries()
	entries[0].SystemPath = "/tmp/replaced"

	got, err := catalog.Lookup(XrayConfig)
	if err != nil {
		t.Fatal(err)
	}
	if got.SystemPath != "/usr/local/etc/xray/config.json" {
		t.Fatalf("catalog mutated through Entries(): %#v", got)
	}
}

func TestServiceConfigDestinationIsSharedWithoutChangingNativeMappings(t *testing.T) {
	catalog, err := ForXrayConfig("/etc/xray/service.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range catalog.Entries() {
		original, _ := Default().Lookup(entry.ID)
		if entry.ID == XrayConfig {
			if entry.SystemPath != "/etc/xray/service.json" || entry.WorkingPath != original.WorkingPath {
				t.Fatal("incorrect Xray mapping")
			}
		} else if entry != original {
			t.Fatal("native destination changed")
		}
	}
	for _, path := range []string{"", "relative", "/", "/etc/../xray/config.json", allowed[1].SystemPath, allowed[0].WorkingPath} {
		if _, err := ForXrayConfig(path); err == nil {
			t.Fatalf("accepted invalid service destination %q", path)
		}
	}
}
