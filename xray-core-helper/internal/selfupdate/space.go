package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// CheckSpace accounts for the complete rollback copy and temporary replacements
// on each destination filesystem. Existing data is counted before shutdown and
// checked again before mutation; normal ENOSPC errors still retain the journal.
func (e *Engine) CheckSpace(op *Operation, rollback bool) error {
	type budget struct {
		path  string
		bytes uint64
	}
	budgets := map[uint64]*budget{}
	add := func(destination, source string) error {
		var bytes uint64
		err := filepath.Walk(source, func(_ string, info os.FileInfo, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				bytes += uint64(info.Size()) + 4096
			} else {
				bytes += 4096
			}
			return nil
		})
		if err != nil {
			return err
		}
		parent := destination
		for {
			info, err := os.Stat(parent)
			if err == nil && info.IsDir() {
				break
			}
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			next := filepath.Dir(parent)
			if next == parent {
				return errors.New("installation_space_unavailable")
			}
			parent = next
		}
		var stat syscall.Stat_t
		if err := syscall.Stat(parent, &stat); err != nil {
			return err
		}
		b := budgets[uint64(stat.Dev)]
		if b == nil {
			b = &budget{path: parent}
			budgets[uint64(stat.Dev)] = b
		}
		b.bytes += bytes
		return nil
	}
	bundle := filepath.Join(e.Store.Dir(op.ID), "bundle")
	if rollback {
		for i, entry := range op.Before {
			if entry.Exists {
				if err := add(e.Files.Path(entry.Path), filepath.Join(e.Store.Dir(op.ID), "snapshot", strconv.Itoa(i))); err != nil {
					return err
				}
			}
		}
	} else {
		for _, target := range Targets(bundle, op.Web) {
			if err := add(e.Files.Path(target.Path), target.Source); err != nil {
				return err
			}
		}
		if !op.SnapshotReady {
			extra := []string{"/etc/xray-core-helper", "/var/lib/xray-core-helper", e.ConfigPath, "/etc/systemd/system/xray-core-helper.service.d/20-installation-paths.conf"}
			// Existing custom DB files may live outside the default data directory.
			if cfg, err := e.loadConfig(bundle); err == nil {
				extra = append(extra, cfg.Storage.Database, cfg.Storage.Database+"-wal", cfg.Storage.Database+"-shm")
			}
			for _, path := range pathsFor(Targets(bundle, op.Web), extra...) {
				if err := add(e.Store.Root, e.Files.Path(path)); err != nil {
					return err
				}
			}
		}
	}
	for _, b := range budgets {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(b.path, &stat); err != nil {
			return errors.New("installation_space_unavailable")
		}
		const margin = uint64(16 << 20)
		if uint64(stat.Bavail)*uint64(stat.Bsize) < b.bytes+margin {
			return errors.New("installation_space_insufficient: not enough free space for snapshot and replacement")
		}
	}
	return nil
}
