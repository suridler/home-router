// Package templatecatalog defines the fixed set of native configuration files
// managed by xray-core-helper. It performs no filesystem access.
package templatecatalog

import (
	"errors"
	"fmt"
	"path/filepath"
)

// ID is a stable template identifier used across helper transports.
type ID string

const (
	XrayConfig ID = "xray-config"
	Nftables   ID = "nftables"
	TProxyUnit ID = "tproxy-unit"
	Sysctl     ID = "sysctl"
)

const WorkingRoot = "/etc/xray-core-helper/templConf"

var ErrUnknownTemplate = errors.New("unknown template")

// Entry maps one stable template ID to its private working copy and trusted
// system destination, fixed for the lifetime of a daemon instance.
type Entry struct {
	ID          ID
	WorkingPath string
	SystemPath  string
}

// Catalog is an immutable, validated set of template mappings.
type Catalog struct {
	entries []Entry
	byID    map[ID]Entry
}

var allowed = []Entry{
	{ID: XrayConfig, WorkingPath: WorkingRoot + "/xray/config.json", SystemPath: "/usr/local/etc/xray/config.json"},
	{ID: Nftables, WorkingPath: WorkingRoot + "/nftables/nftables.conf", SystemPath: "/etc/nftables.conf"},
	{ID: TProxyUnit, WorkingPath: WorkingRoot + "/systemd/xray-tproxy-routing.service", SystemPath: "/etc/systemd/system/xray-tproxy-routing.service"},
	{ID: Sysctl, WorkingPath: WorkingRoot + "/sysctl/99-xray.conf", SystemPath: "/etc/sysctl.d/99-xray.conf"},
}

var allowedByID = indexAllowed(allowed)

// Default returns the standard deployment profile. Daemon path consumers use
// ForXrayConfig with the service-derived destination instead of this profile.
func Default() Catalog {
	catalog, err := newCatalog(allowed)
	if err != nil {
		panic("invalid built-in template catalog: " + err.Error())
	}
	return catalog
}

// newCatalog validates the standard profile against the compiled allowlist.
// ForXrayConfig is the only way to bind another Xray destination; all other
// paths and template IDs remain fixed.
func newCatalog(entries []Entry) (Catalog, error) {
	return catalogFor(entries, allowed[0].SystemPath)
}

// ForXrayConfig binds only Xray's destination to the path discovered from the
// trusted service. Native destinations remain fixed; API callers cannot set paths.
func ForXrayConfig(path string) (Catalog, error) {
	if err := validatePath("system", path); err != nil {
		return Catalog{}, err
	}
	if path == "/" {
		return Catalog{}, errors.New("invalid Xray config destination")
	}
	for _, entry := range allowed {
		if path == entry.WorkingPath || (entry.ID != XrayConfig && path == entry.SystemPath) {
			return Catalog{}, errors.New("Xray config destination conflicts with native catalog")
		}
	}
	entries := append([]Entry(nil), allowed...)
	entries[0].SystemPath = path
	return catalogFor(entries, path)
}

func catalogFor(entries []Entry, xrayPath string) (Catalog, error) {
	byID := make(map[ID]Entry, len(entries))
	seenWorking := make(map[string]ID, len(entries))
	seenSystem := make(map[string]ID, len(entries))
	copyEntries := make([]Entry, 0, len(entries))

	for _, entry := range entries {
		want, ok := allowedByID[entry.ID]
		if !ok {
			return Catalog{}, fmt.Errorf("template %q: %w", entry.ID, ErrUnknownTemplate)
		}
		if entry.ID == XrayConfig {
			want.SystemPath = xrayPath
		}
		if _, exists := byID[entry.ID]; exists {
			return Catalog{}, fmt.Errorf("duplicate template ID %q", entry.ID)
		}
		if err := validatePath("working", entry.WorkingPath); err != nil {
			return Catalog{}, fmt.Errorf("template %q: %w", entry.ID, err)
		}
		if err := validatePath("system", entry.SystemPath); err != nil {
			return Catalog{}, fmt.Errorf("template %q: %w", entry.ID, err)
		}
		if entry.WorkingPath != want.WorkingPath || entry.SystemPath != want.SystemPath {
			return Catalog{}, fmt.Errorf("template %q mapping is outside the allowlist", entry.ID)
		}
		if prior, exists := seenWorking[entry.WorkingPath]; exists {
			return Catalog{}, fmt.Errorf("templates %q and %q share a working path", prior, entry.ID)
		}
		if prior, exists := seenSystem[entry.SystemPath]; exists {
			return Catalog{}, fmt.Errorf("templates %q and %q share a system path", prior, entry.ID)
		}

		byID[entry.ID] = entry
		seenWorking[entry.WorkingPath] = entry.ID
		seenSystem[entry.SystemPath] = entry.ID
		copyEntries = append(copyEntries, entry)
	}

	if len(copyEntries) != len(allowed) {
		return Catalog{}, fmt.Errorf("template catalog must contain all %d allowlisted entries", len(allowed))
	}

	return Catalog{entries: copyEntries, byID: byID}, nil
}

// Entries returns the catalog in its stable display order.
func (catalog Catalog) Entries() []Entry {
	return append([]Entry(nil), catalog.entries...)
}

// Lookup resolves a stable ID without accepting path-like aliases.
func (catalog Catalog) Lookup(id ID) (Entry, error) {
	entry, ok := catalog.byID[id]
	if !ok {
		return Entry{}, fmt.Errorf("template %q: %w", id, ErrUnknownTemplate)
	}
	return entry, nil
}

func validatePath(kind, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s path must be absolute", kind)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("%s path must be canonical", kind)
	}
	return nil
}

func indexAllowed(entries []Entry) map[ID]Entry {
	result := make(map[ID]Entry, len(entries))
	for _, entry := range entries {
		result[entry.ID] = entry
	}
	return result
}
