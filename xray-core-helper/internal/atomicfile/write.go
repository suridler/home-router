// Package atomicfile publishes a file through a synced temporary sibling.
// Callers own directory creation, metadata policy and recovery decisions.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write reports whether rename published the new file even when syncing its
// directory subsequently fails. prepare sets the caller's chosen metadata.
func Write(path string, content []byte, prepare func(*os.File) error) (published bool, err error) {
	return write(path, content, prepare, os.Rename, SyncDir)
}

func write(path string, content []byte, prepare func(*os.File) error, rename func(string, string) error, syncDir func(string) error) (bool, error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".helper-write-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if prepare != nil {
		err = prepare(f)
	}
	if err == nil {
		_, err = f.Write(content)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	if err = rename(f.Name(), path); err != nil {
		return false, err
	}
	return true, syncDir(filepath.Dir(path))
}

func SyncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil {
		return err
	}
	return closeErr
}
