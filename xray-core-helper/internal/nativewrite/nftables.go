// Package nativewrite provides the fixed helper-owned nftables file committer.
// A separate invocation of the daemon binary performs the rename because a
// writable bind mount on /etc/nftables.conf cannot itself be atomically replaced.
package nativewrite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"home-router/xray-core-helper/internal/commandrunner"
)

const Root = "/var/lib/xray-core-helper/native-write"
const Target = "/etc/nftables.conf"
const Unit = "xray-core-helper-nft-commit.service"

var mu sync.Mutex

type intent struct {
	Remove   bool
	Content  []byte
	Mode     uint32
	UID, GID int
}

func Replace(ctx context.Context, raw []byte, mode os.FileMode, uid, gid int) error {
	return submit(ctx, intent{Content: raw, Mode: uint32(mode), UID: uid, GID: gid})
}
func Remove(ctx context.Context) error { return submit(ctx, intent{Remove: true}) }
func submit(ctx context.Context, value intent) error {
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(Root, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(Root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("invalid native write directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("invalid native write owner")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = write(filepath.Join(Root, "intent.json"), raw, 0600, 0, 0); err != nil {
		return err
	}
	runner, err := commandrunner.New("/usr/bin/systemctl", time.Minute)
	if err != nil {
		return err
	}
	if _, err = runner.Run(ctx, []string{"start", Unit}); err != nil {
		return errors.New("nftables file commit failed")
	}
	if err = os.Remove(filepath.Join(Root, "intent.json")); err != nil {
		return err
	}
	return syncDirectory(Root)
}

// Execute has no caller-provided target, command or content arguments. The
// root-only intent is produced by the running helper and never exposed via HTTP.
func Execute() error { return execute(Root, Target) }
func execute(root, target string) error {
	path := filepath.Join(root, "intent.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("invalid native write intent")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("invalid intent owner")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (24<<20)+1))
	if err != nil || len(raw) > 24<<20 {
		return errors.New("invalid intent size")
	}
	var value intent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil {
		return errors.New("invalid intent")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("invalid intent")
	}
	if value.UID < 0 || value.GID < 0 || (!value.Remove && value.Mode == 0) || value.Mode & ^uint32(0777) != 0 {
		return errors.New("invalid intent metadata")
	}
	if i, e := os.Lstat(target); e == nil && !i.Mode().IsRegular() {
		return errors.New("invalid destination")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if value.Remove {
		if err = os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncDirectory(filepath.Dir(target))
	}
	return write(target, value.Content, os.FileMode(value.Mode), value.UID, value.GID)
}
func write(path string, raw []byte, mode os.FileMode, uid, gid int) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".native-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chown(uid, gid); err != nil {
		return err
	}
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
func syncDirectory(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
