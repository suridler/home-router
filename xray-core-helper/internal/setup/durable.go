package setup

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/nativewrite"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The managed directories are root-owned. Reject symlinked ancestors as well as
// final components, and open source files with O_NOFOLLOW.
func safePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("invalid setup path")
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("setup symlink rejected")
		}
	}
	return nil
}

func readSafe(path string) ([]byte, os.FileInfo, error) {
	if err := safePath(path); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, nil, errors.New("invalid setup file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if len(raw) > 16<<20 {
		return nil, nil, errors.New("setup file too large")
	}
	return raw, info, err
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (s *fileStore) replace(path string, raw []byte, mode os.FileMode, uid, gid int) error {
	if path == nativewrite.Target {
		return nativewrite.Replace(context.Background(), raw, mode, uid, gid)
	}
	if err := safePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(filepath.Dir(path))); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".setup-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err = f.Chown(uid, gid); err != nil {
		return err
	}
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if err = s.fail("write", path); err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = s.fail("file-sync", path); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = s.fail("rename", path); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	if err = s.fail("directory-sync", path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
