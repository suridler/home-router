// Package templatebackup stores durable per-template snapshots and restores them.
package templatebackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"home-router/xray-core-helper/internal/nativewrite"
	"home-router/xray-core-helper/internal/templatecatalog"
)

const DefaultRoot = "/var/lib/xray-core-helper/backups/templates"
const maxSnapshotBytes = 1 << 20

type Side string

const (
	SideTemplate Side = "template"
	SideSystem   Side = "system"
)

type State string

const (
	StateCompleted        State = "completed"
	StatePending          State = "pending"
	StateRollback         State = "rollback"
	StateRecoveryRequired State = "recovery_required"
	StateLastKnownGood    State = "last_known_good"
)

var (
	ErrInvalid    = errors.New("invalid backup request")
	ErrNotFound   = errors.New("backup not found")
	ErrConflict   = errors.New("backup generation conflict")
	ErrFilesystem = errors.New("backup filesystem failure")
	ErrValidation = errors.New("backup validation failed")
	ErrSafeApply  = errors.New("system backup requires confirmed group recovery")
)

type Record struct {
	BackupID   string    `json:"backupId"`
	TemplateID string    `json:"templateId"`
	Side       Side      `json:"side"`
	Hash       string    `json:"hash"`
	Mode       uint32    `json:"mode"`
	UID        uint32    `json:"uid"`
	GID        uint32    `json:"gid"`
	Generation uint64    `json:"generation"`
	State      State     `json:"state"`
	CreatedAt  time.Time `json:"createdAt"`
}

type Validator interface {
	Validate(context.Context, templatecatalog.ID, string) (any, error)
}
type validateFunc func(context.Context, templatecatalog.ID, string) error

type Store struct {
	mu       sync.Mutex
	root     string
	entries  map[templatecatalog.ID]templatecatalog.Entry
	retain   func() int
	validate validateFunc
	replace  func(string, []byte, os.FileMode, int, int) error
	now      func() time.Time
}

func New(retain func() int, validator validateFunc) (*Store, error) {
	return NewWithCatalog(retain, validator, templatecatalog.Default())
}

func NewWithCatalog(retain func() int, validator validateFunc, catalog templatecatalog.Catalog) (*Store, error) {
	entries := map[templatecatalog.ID]templatecatalog.Entry{}
	for _, entry := range catalog.Entries() {
		entries[entry.ID] = entry
	}
	return newStore(DefaultRoot, entries, retain, validator)
}

func newStore(root string, entries map[templatecatalog.ID]templatecatalog.Entry, retain func() int, validator validateFunc) (*Store, error) {
	if !filepath.IsAbs(root) || len(entries) == 0 || retain == nil {
		return nil, ErrInvalid
	}
	return &Store{root: root, entries: entries, retain: retain, validate: validator, replace: atomicReplace, now: time.Now}, nil
}

func (store *Store) Snapshot(ctx context.Context, id templatecatalog.ID, side Side, state State) (Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.snapshotLocked(ctx, id, side, state)
}

func (store *Store) snapshotLocked(ctx context.Context, id templatecatalog.ID, side Side, state State) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	entry, ok := store.entries[id]
	if !ok || !validSide(side) || !validState(state) {
		return Record{}, ErrInvalid
	}
	source := entry.SystemPath
	if side == SideTemplate {
		source = entry.WorkingPath
	}
	contents, info, err := readRegular(source)
	if err != nil {
		return Record{}, err
	}
	if err := store.ensureRoots(id); err != nil {
		return Record{}, err
	}
	generation, err := store.nextGeneration(id)
	if err != nil {
		return Record{}, err
	}
	created := store.now().UTC()
	backupID := created.Format("20060102T150405.000000000Z") + "-" + fmt.Sprintf("%020d", generation)
	directory := filepath.Join(store.root, string(id), backupID)
	if err := os.Mkdir(directory, 0700); err != nil {
		return Record{}, ErrFilesystem
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(directory)
			_ = syncDirectory(filepath.Dir(directory))
		}
	}()
	stat := info.Sys().(*syscall.Stat_t)
	sum := sha256.Sum256(contents)
	record := Record{BackupID: backupID, TemplateID: string(id), Side: side, Hash: hex.EncodeToString(sum[:]), Mode: uint32(info.Mode().Perm()), UID: stat.Uid, GID: stat.Gid, Generation: generation, State: state, CreatedAt: created}
	if err := durableFile(filepath.Join(directory, "content"), contents, 0600); err != nil {
		return Record{}, err
	}
	metadata, err := json.Marshal(record)
	if err != nil {
		return Record{}, ErrFilesystem
	}
	if err := durableFile(filepath.Join(directory, "metadata.json"), append(metadata, '\n'), 0600); err != nil {
		return Record{}, err
	}
	if err := syncDirectory(directory); err != nil {
		return Record{}, err
	}
	if err := syncDirectory(filepath.Dir(directory)); err != nil {
		return Record{}, err
	}
	if state == StateCompleted {
		if err := store.cleanupLocked(id); err != nil {
			return Record{}, err
		}
	}
	committed = true
	return record, nil
}

func (store *Store) CompletedGeneration(ctx context.Context, id templatecatalog.ID) (uint64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if _, ok := store.entries[id]; !ok {
		return 0, ErrInvalid
	}
	records, err := store.records(id)
	if err != nil {
		return 0, err
	}
	var generation uint64
	for _, record := range records {
		if record.State == StateCompleted && record.Generation > generation {
			generation = record.Generation
		}
	}
	return generation, nil
}

func (store *Store) List(ctx context.Context, id templatecatalog.ID) ([]Record, uint64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if _, ok := store.entries[id]; !ok {
		return nil, 0, ErrInvalid
	}
	records, err := store.records(id)
	if err != nil {
		return nil, 0, err
	}
	var generation uint64
	for _, record := range records {
		if record.Generation > generation {
			generation = record.Generation
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Generation > records[j].Generation })
	return records, generation, nil
}

func (store *Store) Restore(ctx context.Context, id templatecatalog.ID, backupID string, expectedGeneration uint64) (Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.entries[id]
	if !ok || !safeName(backupID) {
		return Record{}, ErrInvalid
	}
	records, err := store.records(id)
	if err != nil {
		return Record{}, err
	}
	var current uint64
	var selected *Record
	for index := range records {
		record := &records[index]
		if record.Generation > current {
			current = record.Generation
		}
		if record.BackupID == backupID {
			selected = record
		}
	}
	if current != expectedGeneration {
		return Record{}, ErrConflict
	}
	if selected == nil {
		return Record{}, ErrNotFound
	}
	if selected.Side == SideSystem && (id == templatecatalog.Nftables || id == templatecatalog.Sysctl || id == templatecatalog.TProxyUnit) {
		return Record{}, ErrSafeApply
	}
	contentPath := filepath.Join(store.root, string(id), backupID, "content")
	contents, _, err := readRegular(contentPath)
	if err != nil {
		return Record{}, err
	}
	sum := sha256.Sum256(contents)
	if hex.EncodeToString(sum[:]) != selected.Hash {
		return Record{}, ErrFilesystem
	}
	if store.validate != nil {
		if err := store.validate(ctx, id, contentPath); err != nil {
			return Record{}, ErrValidation
		}
	}
	if _, err := store.snapshotLocked(ctx, id, selected.Side, StateRollback); err != nil {
		return Record{}, err
	}
	target := entry.SystemPath
	if selected.Side == SideTemplate {
		target = entry.WorkingPath
	}
	if err := store.replace(target, contents, os.FileMode(selected.Mode), int(selected.UID), int(selected.GID)); err != nil {
		return Record{}, err
	}
	return *selected, nil
}

// RestorePending restores the exact pending safe-apply snapshot without creating
// another generation, so retry after a runtime failure remains idempotent.
func (store *Store) RestorePending(ctx context.Context, id templatecatalog.ID, backupID string) (Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.entries[id]
	if !ok || !safeName(backupID) {
		return Record{}, ErrInvalid
	}
	records, err := store.records(id)
	if err != nil {
		return Record{}, err
	}
	var selected *Record
	for index := range records {
		if records[index].BackupID == backupID {
			selected = &records[index]
			break
		}
	}
	if selected == nil {
		return Record{}, ErrNotFound
	}
	if selected.State != StatePending {
		return Record{}, ErrConflict
	}
	contentPath := filepath.Join(store.root, string(id), backupID, "content")
	contents, _, err := readRegular(contentPath)
	if err != nil {
		return Record{}, err
	}
	sum := sha256.Sum256(contents)
	if hex.EncodeToString(sum[:]) != selected.Hash {
		return Record{}, ErrFilesystem
	}
	// A pending nft SYSTEM snapshot is restored as a file only. Validating
	// it against the changed live kernel state can incorrectly prevent that
	// restoration. Its exact bytes/hash/metadata were durably captured; the
	// operator must reboot before it is treated as runtime recovery.
	if store.validate != nil && !(id == templatecatalog.Nftables && selected.Side == SideSystem) {
		if err := store.validate(ctx, id, contentPath); err != nil {
			return Record{}, ErrValidation
		}
	}
	target := entry.SystemPath
	if selected.Side == SideTemplate {
		target = entry.WorkingPath
	}
	if err := store.replace(target, contents, os.FileMode(selected.Mode), int(selected.UID), int(selected.GID)); err != nil {
		return Record{}, err
	}
	return *selected, nil
}

func (store *Store) SetState(ctx context.Context, id templatecatalog.ID, backupID string, state State) (Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if _, ok := store.entries[id]; !ok || !safeName(backupID) || !validState(state) {
		return Record{}, ErrInvalid
	}
	records, err := store.records(id)
	if err != nil {
		return Record{}, err
	}
	for _, record := range records {
		if record.BackupID != backupID {
			continue
		}
		record.State = state
		encoded, err := json.Marshal(record)
		if err != nil {
			return Record{}, ErrFilesystem
		}
		directory := filepath.Join(store.root, string(id), backupID)
		temporary := filepath.Join(directory, ".metadata.json.tmp")
		_ = os.Remove(temporary)
		if err := durableFile(temporary, append(encoded, '\n'), 0600); err != nil {
			return Record{}, err
		}
		if err := os.Rename(temporary, filepath.Join(directory, "metadata.json")); err != nil {
			return Record{}, ErrFilesystem
		}
		if err := syncDirectory(directory); err != nil {
			return Record{}, err
		}
		if state == StateCompleted {
			if err := store.cleanupLocked(id); err != nil {
				return Record{}, err
			}
		}
		return record, nil
	}
	return Record{}, ErrNotFound
}

func (store *Store) Cleanup(ctx context.Context, id templatecatalog.ID) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return store.cleanupLocked(id)
}
func (store *Store) cleanupLocked(id templatecatalog.ID) error {
	records, err := store.records(id)
	if err != nil {
		return err
	}
	completed := make([]Record, 0)
	for _, record := range records {
		if record.State == StateCompleted {
			completed = append(completed, record)
		}
	}
	sort.Slice(completed, func(i, j int) bool { return completed[i].Generation > completed[j].Generation })
	keep := store.retain()
	if keep < 1 || keep > 100 {
		return ErrInvalid
	}
	if keep >= len(completed) {
		return nil
	}
	for _, record := range completed[keep:] {
		if err := os.RemoveAll(filepath.Join(store.root, string(id), record.BackupID)); err != nil {
			return ErrFilesystem
		}
		if err := syncDirectory(filepath.Join(store.root, string(id))); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) nextGeneration(id templatecatalog.ID) (uint64, error) {
	records, err := store.records(id)
	if err != nil {
		return 0, err
	}
	var max uint64
	for _, r := range records {
		if r.Generation > max {
			max = r.Generation
		}
	}
	return max + 1, nil
}
func (store *Store) records(id templatecatalog.ID) ([]Record, error) {
	directory := filepath.Join(store.root, string(id))
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrFilesystem
	}
	result := make([]Record, 0, len(entries))
	for _, item := range entries {
		if !item.IsDir() || !safeName(item.Name()) {
			continue
		}
		raw, _, err := readRegular(filepath.Join(directory, item.Name(), "metadata.json"))
		if err != nil {
			return nil, err
		}
		var record Record
		if json.Unmarshal(raw, &record) != nil || record.BackupID != item.Name() || record.TemplateID != string(id) || !validSide(record.Side) || !validState(record.State) {
			return nil, ErrFilesystem
		}
		result = append(result, record)
	}
	return result, nil
}

func (store *Store) ensureRoots(id templatecatalog.ID) error {
	if err := secureMkdir(store.root, 0700); err != nil {
		return err
	}
	if err := secureMkdir(filepath.Join(store.root, string(id)), 0700); err != nil {
		return err
	}
	return syncDirectory(store.root)
}
func secureMkdir(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return ErrFilesystem
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrFilesystem
	}
	if err := os.Chmod(path, mode); err != nil {
		return ErrFilesystem
	}
	return nil
}
func validSide(side Side) bool { return side == SideTemplate || side == SideSystem }
func validState(state State) bool {
	return state == StateCompleted || state == StatePending || state == StateRollback || state == StateRecoveryRequired || state == StateLastKnownGood
}
func safeName(value string) bool {
	if value == "" || strings.ContainsAny(value, "/\\") || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func readRegular(path string) ([]byte, os.FileInfo, error) {
	initial, err := os.Lstat(path)
	if err != nil || !initial.Mode().IsRegular() {
		return nil, nil, ErrFilesystem
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, ErrFilesystem
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxSnapshotBytes+1))
	if err != nil || len(contents) > maxSnapshotBytes {
		return nil, nil, ErrFilesystem
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(initial, after) {
		return nil, nil, ErrFilesystem
	}
	return contents, after, nil
}
func durableFile(path string, contents []byte, mode os.FileMode) error {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(mode))
	if err != nil {
		return ErrFilesystem
	}
	file := os.NewFile(uintptr(fd), path)
	_, writeErr := file.Write(contents)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return ErrFilesystem
	}
	return nil
}
func atomicReplace(target string, contents []byte, mode os.FileMode, uid, gid int) error {
	if target == nativewrite.Target {
		return nativewrite.Replace(context.Background(), contents, mode, uid, gid)
	}
	directory := filepath.Dir(target)
	temporary := filepath.Join(directory, "."+filepath.Base(target)+".restore-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := durableFile(temporary, contents, 0600); err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Chmod(temporary, mode.Perm()); err != nil {
		return ErrFilesystem
	}
	if err := os.Chown(temporary, uid, gid); err != nil {
		return ErrFilesystem
	}
	file, err := os.OpenFile(temporary, os.O_RDONLY, 0)
	if err != nil {
		return ErrFilesystem
	}
	err = file.Sync()
	_ = file.Close()
	if err != nil {
		return ErrFilesystem
	}
	if err := os.Rename(temporary, target); err != nil {
		return ErrFilesystem
	}
	return syncDirectory(directory)
}
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return ErrFilesystem
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return ErrFilesystem
	}
	return nil
}
