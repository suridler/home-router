// Package selfupdate owns the standalone installation journal. It deliberately
// does not store recovery evidence in the database that rollback restores.
package selfupdate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

const StateRoot = "/var/lib/xray-core-helper-update"
const ManifestPath = "/usr/share/xray-core-helper/release.json"

var validID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var ErrBusy = errors.New("update_busy: another installation or recovery operation is pending")

type Attempt struct {
	ID           string     `json:"id"`
	Action       string     `json:"action"`
	State        string     `json:"state"`
	Stage        string     `json:"stage"`
	Error        string     `json:"error,omitempty"`
	BootID       string     `json:"bootId,omitempty"`
	InvocationID string     `json:"invocationId,omitempty"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

type Operation struct {
	SchemaVersion  int       `json:"schemaVersion"`
	ID             string    `json:"id"`
	SourceVersion  string    `json:"sourceVersion,omitempty"`
	TargetVersion  string    `json:"targetVersion"`
	Commit         string    `json:"commit"`
	Arch           string    `json:"arch"`
	MinimumVersion string    `json:"minimum_installable_version,omitempty"`
	Web            bool      `json:"web"`
	Fresh          bool      `json:"fresh"`
	State          string    `json:"state"`
	Stage          string    `json:"stage"`
	Error          string    `json:"error,omitempty"`
	SnapshotReady  bool      `json:"snapshotReady"`
	CreatedAt      time.Time `json:"createdAt"`
	Attempts       []Attempt `json:"attempts"`
	// Fingerprints belong to the private journal, never to the public API.
	Before                  []Entry `json:"before,omitempty"`
	CandidateDigest         string  `json:"candidateDigest,omitempty"`
	InstallerDigest         string  `json:"installerDigest,omitempty"`
	InstallationPathsDigest string  `json:"installationPathsDigest,omitempty"`
	ConfigPath              string  `json:"configPath"`
}

type Store struct{ Root string }

func NewStore(root string) *Store { return &Store{Root: root} }
func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func (s *Store) Dir(id string) string { return filepath.Join(s.Root, "operations", id) }

// Lock is also used for runtime.lock: daemon/standalone retain this lock for
// their entire writing lifetime. O_NOFOLLOW rejects a substituted lock file.
func Lock(path string) (func(), error) {
	if err := secureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrBusy
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func secureDir(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("installation path must be absolute")
	}
	// Reject symlinks at every existing component before creating private state.
	for at := filepath.Clean(path); at != "/"; at = filepath.Dir(at) {
		st, err := os.Lstat(at)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe installation directory")
		}
	}
	return os.MkdirAll(path, 0700)
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	if err := secureDir(filepath.Dir(path)); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return errors.New("unsafe installation file")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".install-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
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
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(raw, '\n'), 0600)
}
func readJSON(path string, v any) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 16<<20 {
		return errors.New("invalid installation journal")
	}
	d := json.NewDecoder(io.LimitReader(f, 16<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("invalid journal trailing data")
	}
	return nil
}
func (s *Store) Save(op *Operation) error {
	if !validID.MatchString(op.ID) || op.SchemaVersion != 1 {
		return errors.New("invalid operation")
	}
	if err := writeJSON(filepath.Join(s.Dir(op.ID), "state.json"), op); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.Root, "current"), []byte(op.ID), 0600)
}
func (s *Store) Load() (*Operation, error) {
	f, e := os.OpenFile(filepath.Join(s.Root, "current"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 33))
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return s.LoadID(string(b))
}
func (s *Store) LoadID(id string) (*Operation, error) {
	if !validID.MatchString(id) {
		return nil, errors.New("invalid operation id")
	}
	var op Operation
	if e := readJSON(filepath.Join(s.Dir(id), "state.json"), &op); e != nil {
		return nil, e
	}
	if op.ID != id || op.SchemaVersion != 1 {
		return nil, errors.New("unsupported operation manifest")
	}
	return &op, nil
}
func terminal(state string) bool { return state == "succeeded" || state == "rolled_back" }
func (op *Operation) Set(stage string) {
	op.Stage = stage
	if len(op.Attempts) > 0 {
		op.Attempts[len(op.Attempts)-1].Stage = stage
	}
}
func (op *Operation) Fail(code string) {
	now := time.Now().UTC()
	op.State = "failed"
	op.Error = code
	if len(op.Attempts) > 0 {
		a := &op.Attempts[len(op.Attempts)-1]
		a.State = "failed"
		a.Error = code
		a.FinishedAt = &now
	}
}
func (op *Operation) Finish(state string) {
	now := time.Now().UTC()
	op.State = state
	op.Error = ""
	op.Set("complete")
	if len(op.Attempts) > 0 {
		a := &op.Attempts[len(op.Attempts)-1]
		a.State = state
		a.FinishedAt = &now
	}
}
func (op *Operation) Public() *Operation {
	if op == nil {
		return nil
	}
	v := *op
	v.Before = nil
	v.CandidateDigest = ""
	v.InstallerDigest = ""
	v.InstallationPathsDigest = ""
	v.ConfigPath = ""
	return &v
}
func requiredID(id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid operation/action id")
	}
	return nil
}
