package logfileprep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"home-router/xray-core-helper/internal/api"
)

func TestPrepareCreatesBothManagedLogsWithExactModesAndIdentity(t *testing.T) {
	root := t.TempDir()
	makeLogParent(t, root)
	identity := testIdentity()
	preparer := testPreparer(t, root, identity, nil, nil)

	snapshot, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetFile))
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	defer snapshot.Commit()

	assertMetadata(t, filepath.Join(root, AccessLogPath), identity, fileMode)
	assertMetadata(t, filepath.Join(root, ErrorLogPath), identity, fileMode)
	assertMetadata(t, filepath.Join(root, ManagedDirectory), identity, directoryMode)
	if AccessLogPath != ManagedDirectory+"/access.log" || ErrorLogPath != ManagedDirectory+"/error.log" {
		t.Fatalf("managed paths changed: %q and %q", AccessLogPath, ErrorLogPath)
	}
}

func TestPreparePreservesExistingContentAndRestoresPriorMetadata(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(makeLogParent(t, root), "xray")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	identity := testIdentity()
	access := filepath.Join(dir, "access.log")
	errorLog := filepath.Join(dir, "error.log")
	writeExisting(t, access, "access fixture content\n", 0600)
	writeExisting(t, errorLog, "error fixture content\n", 0600)
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	previousAccess := readMetadataAt(t, access)
	previousError := readMetadataAt(t, errorLog)
	previousDirectory := readMetadataAt(t, dir)

	preparer := testPreparer(t, root, identity, nil, nil)
	snapshot, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetFile))
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	assertContent(t, access, "access fixture content\n")
	assertContent(t, errorLog, "error fixture content\n")
	assertMetadata(t, access, identity, fileMode)
	assertMetadata(t, errorLog, identity, fileMode)
	assertMetadata(t, dir, identity, directoryMode)

	if err := snapshot.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	assertContent(t, access, "access fixture content\n")
	assertContent(t, errorLog, "error fixture content\n")
	assertMode(t, access, 0600)
	assertMode(t, errorLog, 0600)
	assertMode(t, dir, 0700)
	assertOwner(t, access, previousAccess.uid, previousAccess.gid)
	assertOwner(t, errorLog, previousError.uid, previousError.gid)
	assertOwner(t, dir, previousDirectory.uid, previousDirectory.gid)
}

func TestPrepareSelectionCreatesOnlyRequestedFileAndNoOpsForNonFileTargets(t *testing.T) {
	t.Run("one file", func(t *testing.T) {
		root := t.TempDir()
		makeLogParent(t, root)
		preparer := testPreparer(t, root, testIdentity(), nil, nil)
		snapshot, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
		if err != nil {
			t.Fatal(err)
		}
		defer snapshot.Commit()
		assertRegularExists(t, filepath.Join(root, AccessLogPath))
		assertAbsent(t, filepath.Join(root, ErrorLogPath))
	})

	for _, modes := range []struct {
		name          string
		access, error api.LogTargetMode
	}{
		{name: "both disabled", access: api.LogTargetDisabled, error: api.LogTargetDisabled},
		{name: "both journal", access: api.LogTargetJournal, error: api.LogTargetJournal},
		{name: "mixed non-file", access: api.LogTargetDisabled, error: api.LogTargetJournal},
	} {
		t.Run(modes.name, func(t *testing.T) {
			opened := false
			preparer := testPreparer(t, t.TempDir(), testIdentity(), func() { opened = true }, nil)
			snapshot, err := preparer.Prepare(context.Background(), logRequest(modes.access, modes.error))
			if err != nil {
				t.Fatalf("Prepare() error = %v", err)
			}
			if opened {
				t.Fatal("non-file selection opened the filesystem")
			}
			if err := snapshot.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("unsupported mode", func(t *testing.T) {
		preparer := testPreparer(t, t.TempDir(), testIdentity(), nil, nil)
		_, err := preparer.Prepare(context.Background(), logRequest("arbitrary-path", api.LogTargetJournal))
		if !errors.Is(err, ErrInvalidSelection) {
			t.Fatalf("Prepare() error = %v; want ErrInvalidSelection", err)
		}
	})
}

func TestPrepareRejectsInvalidIdentityWithoutFilesystemAccess(t *testing.T) {
	for _, identity := range []Identity{{UID: 0, GID: 1000}, {UID: 1000, GID: 0}, {UID: -1, GID: 1000},
		{UID: int(^uint32(0)) + 1, GID: 1000}} {
		opened := false
		preparer := testPreparer(t, t.TempDir(), identity, func() { opened = true }, nil)
		_, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
		if !errors.Is(err, ErrInvalidIdentity) {
			t.Errorf("identity %#v error = %v; want ErrInvalidIdentity", identity, err)
		}
		if opened {
			t.Errorf("identity %#v opened filesystem", identity)
		}
	}
}

func TestPrepareRedactsIdentityAndFilesystemErrors(t *testing.T) {
	const secret = "fixture-private-identity"
	t.Run("identity source", func(t *testing.T) {
		preparer, err := New(IdentityResolverFunc(func(context.Context) (Identity, error) {
			return Identity{}, errors.New(secret)
		}), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
		if !errors.Is(err, ErrIdentityUnavailable) || strings.Contains(err.Error(), secret) {
			t.Fatalf("Prepare() error = %v; want redacted ErrIdentityUnavailable", err)
		}
	})

	t.Run("filesystem", func(t *testing.T) {
		root := t.TempDir()
		makeLogParent(t, root)
		files := &testFileSystem{root: root, fail: map[string]error{"open:error.log": errors.New(secret)}}
		preparer := testPreparer(t, root, testIdentity(), nil, files)
		_, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetFile))
		if !errors.Is(err, ErrPreparation) || strings.Contains(err.Error(), secret) {
			t.Fatalf("Prepare() error = %v; want redacted ErrPreparation", err)
		}
		assertAbsent(t, filepath.Join(root, AccessLogPath))
		assertAbsent(t, filepath.Join(root, ManagedDirectory))
	})
}

func TestPrepareRejectsNonDirectoryRootAndSymlinkComponents(t *testing.T) {
	t.Run("non-directory xray path", func(t *testing.T) {
		root := t.TempDir()
		parent := makeLogParent(t, root)
		if err := os.WriteFile(filepath.Join(parent, "xray"), []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		preparer := testPreparer(t, root, testIdentity(), nil, nil)
		_, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
		if !errors.Is(err, ErrUnsafeFilesystem) {
			t.Fatalf("Prepare() error = %v; want ErrUnsafeFilesystem", err)
		}
		assertContent(t, filepath.Join(parent, "xray"), "not a directory")
	})

	for _, component := range []string{"var", filepath.Join("var", "log"), filepath.Join("var", "log", "xray")} {
		t.Run("symlink "+component, func(t *testing.T) {
			root := t.TempDir()
			target := t.TempDir()
			link := filepath.Join(root, component)
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			preparer := testPreparer(t, root, testIdentity(), nil, nil)
			_, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
			if !errors.Is(err, ErrUnsafeFilesystem) {
				t.Fatalf("Prepare() error = %v; want ErrUnsafeFilesystem", err)
			}
			assertAbsent(t, filepath.Join(target, "access.log"))
		})
	}
}

func TestPrepareRejectsSymlinkFinalLogTargets(t *testing.T) {
	for _, target := range []string{"access.log", "error.log"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(makeLogParent(t, root), "xray")
			if err := os.Mkdir(dir, 0755); err != nil {
				t.Fatal(err)
			}
			external := filepath.Join(t.TempDir(), "external.log")
			writeExisting(t, external, "must not change\n", 0600)
			if err := os.Symlink(external, filepath.Join(dir, target)); err != nil {
				t.Fatal(err)
			}
			preparer := testPreparer(t, root, testIdentity(), nil, nil)
			_, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetFile))
			if !errors.Is(err, ErrUnsafeFilesystem) {
				t.Fatalf("Prepare() error = %v; want ErrUnsafeFilesystem", err)
			}
			assertContent(t, external, "must not change\n")
			if target == "access.log" {
				assertAbsent(t, filepath.Join(dir, "error.log"))
			} else {
				assertAbsent(t, filepath.Join(dir, "access.log"))
			}
		})
	}
}

func TestPrepareRejectsNonRegularManagedLog(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(makeLogParent(t, root), "xray")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "access.log"), 0700); err != nil {
		t.Fatal(err)
	}
	preparer := testPreparer(t, root, testIdentity(), nil, nil)
	_, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
	if !errors.Is(err, ErrUnsafeFilesystem) {
		t.Fatalf("Prepare() error = %v; want ErrUnsafeFilesystem", err)
	}
	assertAbsent(t, filepath.Join(dir, "error.log"))
}

func TestPreparePartialFailureRollsBackMetadataAndCreatedEntries(t *testing.T) {
	t.Run("metadata", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(makeLogParent(t, root), "xray")
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		access := filepath.Join(dir, "access.log")
		errorLog := filepath.Join(dir, "error.log")
		writeExisting(t, access, "old access\n", 0600)
		writeExisting(t, errorLog, "old error\n", 0600)
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		previousAccess := readMetadataAt(t, access)
		previousError := readMetadataAt(t, errorLog)
		previousDirectory := readMetadataAt(t, dir)

		files := &testFileSystem{root: root, fail: map[string]error{"chmod:error.log:0640": errors.New("fixture failure")}}
		preparer := testPreparer(t, root, testIdentity(), nil, files)
		_, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetFile))
		if !errors.Is(err, ErrPreparation) || errors.Is(err, ErrRollbackFailed) {
			t.Fatalf("Prepare() error = %v; want successfully rolled-back ErrPreparation", err)
		}
		assertMode(t, access, 0600)
		assertMode(t, errorLog, 0600)
		assertMode(t, dir, 0700)
		assertOwner(t, access, previousAccess.uid, previousAccess.gid)
		assertOwner(t, errorLog, previousError.uid, previousError.gid)
		assertOwner(t, dir, previousDirectory.uid, previousDirectory.gid)
		assertContent(t, access, "old access\n")
		assertContent(t, errorLog, "old error\n")
	})

	t.Run("remove only created entries", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(makeLogParent(t, root), "xray")
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		existing := filepath.Join(dir, "error.log")
		writeExisting(t, existing, "keep pre-existing content\n", 0600)
		files := &testFileSystem{root: root, fail: map[string]error{"open:error.log": errors.New("injected failure")}}
		preparer := testPreparer(t, root, testIdentity(), nil, files)
		_, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetFile))
		if !errors.Is(err, ErrPreparation) {
			t.Fatalf("Prepare() error = %v; want ErrPreparation", err)
		}
		assertAbsent(t, filepath.Join(dir, "access.log"))
		assertContent(t, existing, "keep pre-existing content\n")
		assertMode(t, existing, 0600)
		assertMode(t, dir, 0755)
	})
}

func TestPrepareIsIdempotentAndRollbackDoesNotRemoveReplacedEntries(t *testing.T) {
	root := t.TempDir()
	makeLogParent(t, root)
	preparer := testPreparer(t, root, testIdentity(), nil, nil)
	request := logRequest(api.LogTargetFile, api.LogTargetFile)

	first, err := preparer.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, AccessLogPath), []byte("append-only fixture\n"), 0640); err != nil {
		t.Fatal(err)
	}
	second, err := preparer.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Commit(); err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(root, AccessLogPath), "append-only fixture\n")
	assertMetadata(t, filepath.Join(root, AccessLogPath), testIdentity(), fileMode)

	third, err := preparer.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := third.Commit(); !errors.Is(err, ErrRollbackFailed) {
			t.Errorf("Commit() after concurrent replacement error = %v; want ErrRollbackFailed", err)
		}
	})
	original := filepath.Join(root, AccessLogPath)
	replacement := filepath.Join(root, "replacement.log")
	if err := os.Rename(original, replacement); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("concurrent replacement\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := third.Rollback(); !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("Rollback() error = %v; want failure on replaced name", err)
	}
	assertContent(t, original, "concurrent replacement\n")
	assertContent(t, replacement, "append-only fixture\n")
}

func TestCommitRejectsConcurrentReplacement(t *testing.T) {
	root := t.TempDir()
	makeLogParent(t, root)
	preparer := testPreparer(t, root, testIdentity(), nil, nil)
	snapshot, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, AccessLogPath)
	replacement := filepath.Join(root, "replacement.log")
	if err := os.Rename(original, replacement); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("concurrent replacement\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Commit(); !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("Commit() error = %v; want replacement detection", err)
	}
	assertContent(t, original, "concurrent replacement\n")
	assertRegularExists(t, replacement)
}

func logRequest(access, errorMode api.LogTargetMode) api.LogConfigurationRequest {
	return api.LogConfigurationRequest{
		Access: api.LogTarget{Mode: access},
		Error:  api.LogTarget{Mode: errorMode},
	}
}

func testIdentity() Identity {
	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 {
		uid, gid = 21001, 21002
	}
	if gid == 0 {
		gid = 21002
	}
	return Identity{UID: uid, GID: gid}
}

func testPreparer(t *testing.T, root string, identity Identity, onOpen func(), files *testFileSystem) *Preparer {
	t.Helper()
	if files == nil {
		files = &testFileSystem{root: root}
	}
	if onOpen != nil {
		files.onOpen = onOpen
	}
	preparer, err := New(IdentityResolverFunc(func(context.Context) (Identity, error) {
		return identity, nil
	}), files)
	if err != nil {
		t.Fatal(err)
	}
	return preparer
}

func makeLogParent(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "var", "log")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeExisting(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", filepath.Base(path), err)
	}
	if string(content) != want {
		t.Fatalf("content of %q = %q; want %q", filepath.Base(path), content, want)
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lstat(%q) = %v; want absent", filepath.Base(path), err)
	}
}

func assertRegularExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("Lstat(%q) = %v, %v; want regular file", filepath.Base(path), info, err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != want {
		t.Fatalf("mode of %q = %v, %v; want %04o", filepath.Base(path), info, err, want)
	}
}

func assertMetadata(t *testing.T, path string, identity Identity, wantMode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", filepath.Base(path), err)
	}
	if info.Mode().Perm() != wantMode ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Fatalf("%q mode = %v; want exact %04o", filepath.Base(path), info.Mode(), wantMode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat metadata for %q has unexpected type %T", filepath.Base(path), info.Sys())
	}
	if int(stat.Uid) != identity.UID || int(stat.Gid) != identity.GID {
		t.Fatalf("owner of %q = %d:%d; want %d:%d", filepath.Base(path), stat.Uid, stat.Gid, identity.UID, identity.GID)
	}
}

func assertOwner(t *testing.T, path string, uid, gid int) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", filepath.Base(path), err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid || int(stat.Gid) != gid {
		t.Fatalf("owner of %q = %#v; want %d:%d", filepath.Base(path), info.Sys(), uid, gid)
	}
}

func readMetadataAt(t *testing.T, path string) metadata {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := readMetadata(info)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type testFileSystem struct {
	root   string
	mu     sync.Mutex
	fail   map[string]error
	onOpen func()
}

func (files *testFileSystem) OpenRoot() (Directory, error) {
	if files.onOpen != nil {
		files.onOpen()
	}
	fd, err := syscall.Open(files.root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &testDirectory{Directory: &osDirectory{file: os.NewFile(uintptr(fd), files.root)}, files: files}, nil
}

func (files *testFileSystem) failure(operation string) error {
	files.mu.Lock()
	defer files.mu.Unlock()
	err := files.fail[operation]
	delete(files.fail, operation)
	return err
}

type testDirectory struct {
	Directory
	files *testFileSystem
}

func (directory *testDirectory) OpenDirectory(name string) (Directory, error) {
	opened, err := directory.Directory.OpenDirectory(name)
	if err != nil {
		return nil, err
	}
	return &testDirectory{Directory: opened, files: directory.files}, nil
}

func (directory *testDirectory) OpenFile(name string, flags int, mode os.FileMode) (File, error) {
	if err := directory.files.failure("open:" + name); err != nil {
		return nil, err
	}
	opened, err := directory.Directory.OpenFile(name, flags, mode)
	if err != nil {
		return nil, err
	}
	return &testFile{File: opened, files: directory.files, name: name}, nil
}

type testFile struct {
	File
	files *testFileSystem
	name  string
}

func (file *testFile) Chmod(mode os.FileMode) error {
	if file.name == "error.log" && mode.Perm() == fileMode {
		if err := file.files.failure("chmod:error.log:0640"); err != nil {
			return err
		}
	}
	return file.File.Chmod(mode)
}
