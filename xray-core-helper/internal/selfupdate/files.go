package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type Entry struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Hash   string `json:"hash,omitempty"`
}

type Target struct {
	Path, Source string
	Mode         os.FileMode
}
type Files struct{ SystemRoot string }

func (f Files) Path(path string) string {
	return filepath.Join(f.SystemRoot, strings.TrimPrefix(path, "/"))
}

func digest(path string) (string, error) {
	h := sha256.New()
	err := filepath.Walk(path, func(p string, st os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(path, p)
		if st.Mode()&os.ModeSymlink != 0 {
			link, e := os.Readlink(p)
			if e != nil {
				return e
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(p), link))
			if filepath.IsAbs(link) || !(resolved == path || strings.HasPrefix(resolved, path+string(os.PathSeparator))) {
				return errors.New("unsafe link in managed tree")
			}
			fmt.Fprintf(h, "%s:link:%s\n", rel, link)
			return nil
		}
		if !st.IsDir() && !st.Mode().IsRegular() {
			return errors.New("special file in installation tree")
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("file metadata unavailable")
		}
		fmt.Fprintf(h, "%s:%o:%d:%d\n", rel, st.Mode().Perm(), sys.Uid, sys.Gid)
		if st.Mode().IsRegular() {
			b, e := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if e != nil {
				return e
			}
			_, e = io.Copy(h, b)
			b.Close()
			if e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fingerprint(path string) (Entry, error) {
	e := Entry{Path: path}
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return e, errors.New("managed destination is a symlink")
	}
	e.Exists = true
	e.Hash, err = digest(path)
	return e, err
}

func (f Files) Inspect(paths []string) ([]Entry, error) {
	var entries []Entry
	for _, p := range paths {
		e, err := fingerprint(f.Path(p))
		if err != nil {
			return nil, err
		}
		e.Path = p
		entries = append(entries, e)
	}
	return entries, nil
}
func (f Files) Unchanged(before []Entry) error {
	for _, old := range before {
		now, err := fingerprint(f.Path(old.Path))
		if err != nil {
			return err
		}
		if now.Exists != old.Exists || now.Hash != old.Hash {
			return errors.New("installation_drift: installed files changed after preparation")
		}
	}
	return nil
}

// copyTree copies content and complete POSIX metadata. Candidate npm .bin links
// must remain inside the candidate tree; external links are never followed.
func copyTree(src, dst string) error {
	var dirs []string
	err := filepath.Walk(src, func(path string, st os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		to := filepath.Join(dst, rel)
		if st.Mode()&os.ModeSymlink != 0 {
			link, e := os.Readlink(path)
			if e != nil {
				return e
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), link))
			if filepath.IsAbs(link) || !strings.HasPrefix(resolved, src+string(os.PathSeparator)) {
				return errors.New("unsafe managed link")
			}
			return os.Symlink(link, to)
		}
		if st.IsDir() {
			if e := os.MkdirAll(to, 0700); e != nil {
				return e
			}
			dirs = append(dirs, path)
			return nil
		}
		if !st.Mode().IsRegular() {
			return errors.New("special file in bundle")
		}
		in, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if e != nil {
			return e
		}
		defer in.Close()
		out, e := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = io.Copy(out, in)
		if e == nil {
			e = out.Sync()
		}
		ce := out.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			return e
		}
		return metadata(to, st)
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		st, e := os.Lstat(dirs[i])
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(src, dirs[i])
		if e = metadata(filepath.Join(dst, rel), st); e != nil {
			return e
		}
		if e = syncDir(filepath.Join(dst, rel)); e != nil {
			return e
		}
	}
	return nil
}
func metadata(path string, st os.FileInfo) error {
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("metadata unavailable")
	}
	if err := os.Chown(path, int(s.Uid), int(s.Gid)); err != nil {
		return err
	}
	if err := os.Chmod(path, st.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
		return err
	}
	if st.IsDir() {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func (f Files) Snapshot(op *Operation, dir string, save func() error) error {
	if op.SnapshotReady {
		return nil
	}
	// Incomplete snapshots never become rollback sources. Rebuild only before
	// any writes to installed destinations have been admitted.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := secureDir(dir); err != nil {
		return err
	}
	for i, e := range op.Before {
		if e.Exists {
			if err := copyTree(f.Path(e.Path), filepath.Join(dir, fmt.Sprint(i))); err != nil {
				return err
			}
			h, err := digest(filepath.Join(dir, fmt.Sprint(i)))
			if err != nil || h != e.Hash {
				return errors.New("snapshot_drift: source changed during snapshot")
			}
		}
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	op.SnapshotReady = true
	return save()
}

func (f Files) Restore(op *Operation, dir string, save func() error) error {
	if !op.SnapshotReady {
		return errors.New("snapshot_unavailable: complete rollback snapshot is missing")
	}
	// Validate EVERY entry before restoring the first destination.
	for i, e := range op.Before {
		if e.Exists {
			h, err := digest(filepath.Join(dir, fmt.Sprint(i)))
			if err != nil || h != e.Hash {
				return errors.New("snapshot_invalid: rollback snapshot is damaged")
			}
		}
	}
	for i, e := range op.Before {
		op.Set("rollback:" + e.Path)
		if err := save(); err != nil {
			return err
		}
		if e.Exists {
			if err := replaceTree(filepath.Join(dir, fmt.Sprint(i)), f.Path(e.Path)); err != nil {
				return err
			}
		} else {
			if err := os.RemoveAll(f.Path(e.Path)); err != nil {
				return err
			}
			if err := syncExistingParent(f.Path(e.Path)); err != nil {
				return err
			}
		}
	}
	return nil
}

func syncExistingParent(path string) error {
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		if st, e := os.Stat(p); e == nil && st.IsDir() {
			return syncDir(p)
		}
		if p == "/" {
			return nil
		}
	}
}

// Directories use two renames with a durable journal intent recorded by the
// caller. A crash between them is recoverable; it is never called atomic bundle
// replacement. Temporary sibling names are deterministic for retry cleanup.
func replaceTree(src, dst string, installMode ...os.FileMode) error {
	if err := secureDir(filepath.Dir(dst)); err != nil {
		return err
	}
	if st, e := os.Lstat(dst); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe destination")
	}
	tag := sha256.Sum256([]byte(src))
	suffix := hex.EncodeToString(tag[:8])
	tmp, old := dst+".helper-new-"+suffix, dst+".helper-old-"+suffix
	for _, p := range []string{tmp, old} {
		if st, e := os.Lstat(p); e == nil && st.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe temporary destination")
		}
	}
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := copyTree(src, tmp); err != nil {
		return err
	}
	if len(installMode) > 0 {
		if err := normalizeInstall(tmp, installMode[0]); err != nil {
			return err
		}
	}
	st, err := os.Lstat(dst)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && st.IsDir() {
		if err = os.RemoveAll(old); err != nil {
			return err
		}
		if err = os.Rename(dst, old); err != nil {
			return err
		}
		if err = syncDir(filepath.Dir(dst)); err != nil {
			return err
		}
	}
	if err = os.Rename(tmp, dst); err != nil {
		return err
	}
	if err = syncDir(filepath.Dir(dst)); err != nil {
		return err
	}
	return os.RemoveAll(old)
}

func Targets(bundle string, web bool) []Target {
	h := filepath.Join(bundle, "xray-core-helper")
	v := []Target{
		{"/usr/local/bin/xray-core-helper", filepath.Join(h, "bin/xray-core-helper"), 0755},
		{"/usr/local/sbin/xray-core-helperd", filepath.Join(h, "bin/xray-core-helperd"), 0755},
		{"/usr/local/bin/domain-list-community", filepath.Join(h, "bin/domain-list-community"), 0755},
		{"/usr/local/share/licenses/xray-core-helper/domain-list-community.txt", filepath.Join(h, "licenses/domain-list-community.txt"), 0644},
		{"/usr/share/xray-core-helper/templConf", filepath.Join(h, "templConf"), 0},
		{ManifestPath, filepath.Join(bundle, "release.json"), 0644},
		{"/etc/sysusers.d/xray-core-helper.conf", filepath.Join(h, "packaging/sysusers.d/xray-core-helper.conf"), 0644},
		{"/etc/tmpfiles.d/xray-core-helper.conf", filepath.Join(h, "packaging/tmpfiles.d/xray-core-helper.conf"), 0644},
		{"/usr/local/libexec/xray-core-helper-nft-load", filepath.Join(h, "packaging/xray-core-helper-nft-load.py"), 0755},
	}
	for _, unit := range []string{"xray-core-helper.service", "xray-core-helper-nft-commit.service", "xray-core-helper-sysctl.service", "xray-core-helper-rollback.service", "xray-core-helper-rollback.timer"} {
		v = append(v, Target{"/etc/systemd/system/" + unit, filepath.Join(h, "packaging/systemd", unit), 0644})
	}
	if web {
		v = append(v, Target{"/opt/xrayui-web", filepath.Join(bundle, "xrayui-web"), 0}, Target{"/etc/systemd/system/xrayui-web.service", filepath.Join(bundle, "xrayui-web/xrayui-web.service"), 0644}, Target{"/etc/sysusers.d/xrayui-web.conf", filepath.Join(bundle, "xrayui-web/packaging/sysusers.d/xrayui-web.conf"), 0644})
	}
	return v
}
func pathsFor(targets []Target, extra ...string) []string {
	var paths []string
	for _, t := range targets {
		paths = append(paths, t.Path)
	}
	paths = append(paths, extra...)
	sort.Strings(paths)
	var result []string
	for _, p := range paths {
		covered := false
		for _, r := range result {
			if p == r || strings.HasPrefix(p, r+"/") {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, p)
		}
	}
	return result
}

func (f Files) Apply(op *Operation, targets []Target, save func() error) error {
	for _, t := range targets {
		op.Set("replace:" + t.Path)
		if err := save(); err != nil {
			return err
		}
		if err := replaceTree(t.Source, f.Path(t.Path), t.Mode); err != nil {
			return err
		}
	}
	return nil
}

// Installed executables and web resources must never retain the ownership of
// an operator-provided unpacked bundle. Snapshot/restore preserves original
// metadata separately and does not use this normalization.
func normalizeInstall(root string, rootMode os.FileMode) error {
	return filepath.Walk(root, func(path string, st os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return os.Lchown(path, os.Geteuid(), os.Getegid())
		}
		if err = os.Chown(path, os.Geteuid(), os.Getegid()); err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if st.IsDir() {
			mode = 0755
		} else if path == root && rootMode != 0 {
			mode = rootMode
		}
		if err = os.Chmod(path, mode); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		return file.Sync()
	})
}

// Remove only deterministic siblings created by this operation's replacement
// sources. Never sweep unrelated administrator files by a glob.
func (f Files) CleanupCandidate(targets []Target) error {
	for _, target := range targets {
		sum := sha256.Sum256([]byte(target.Source))
		tag := hex.EncodeToString(sum[:8])
		dst := f.Path(target.Path)
		for _, suffix := range []string{".helper-new-", ".helper-old-"} {
			if err := os.RemoveAll(dst + suffix + tag); err != nil {
				return err
			}
		}
		if err := syncExistingParent(dst); err != nil {
			return err
		}
	}
	return nil
}
