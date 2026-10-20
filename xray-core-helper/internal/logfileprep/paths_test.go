package logfileprep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
)

func TestPathsLocalValidatesConstructorInputs(t *testing.T) {
	valid := filepath.Join(t.TempDir(), "access.log")
	other := filepath.Join(t.TempDir(), "error.log")
	resolver := IdentityResolverFunc(func(context.Context) (Identity, error) {
		return testIdentity(), nil
	})

	if _, err := NewWithPaths(nil, nil, valid, other); !errors.Is(err, ErrIdentityUnavailable) {
		t.Fatalf("NewWithPaths() error = %v; want ErrIdentityUnavailable", err)
	}
	if _, err := NewWithPaths(resolver, nil, valid, other); err != nil {
		t.Fatalf("NewWithPaths() error = %v; want nil", err)
	}

	invalid := []string{"", "none", "relative.log", "/tmp/../tmp/access.log", "/"}
	for _, value := range invalid {
		if _, err := NewWithPaths(resolver, nil, value, other); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("NewWithPaths(access=%q) error = %v; want ErrInvalidPath", value, err)
		}
		if _, err := NewWithPaths(resolver, nil, valid, value); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("NewWithPaths(error=%q) error = %v; want ErrInvalidPath", value, err)
		}
	}
}

func TestPathsLocalNonFileModesDoNoWork(t *testing.T) {
	root := t.TempDir()
	files := &pathsLocalCountingFileSystem{
		FileSystem: &testFileSystem{root: root},
	}
	identityCalls := 0
	preparer, err := NewWithPaths(IdentityResolverFunc(func(context.Context) (Identity, error) {
		identityCalls++
		return testIdentity(), nil
	}), files, "/access.log", "/error.log")
	if err != nil {
		t.Fatal(err)
	}

	requests := []api.LogConfigurationRequest{
		logRequest(api.LogTargetDisabled, api.LogTargetDisabled),
		logRequest(api.LogTargetJournal, api.LogTargetJournal),
		logRequest(api.LogTargetDisabled, api.LogTargetJournal),
	}
	for _, request := range requests {
		snapshot, err := preparer.Prepare(context.Background(), request)
		if err != nil {
			t.Fatalf("Prepare(%#v) error = %v", request, err)
		}
		if err := snapshot.Rollback(); err != nil {
			t.Fatalf("Rollback() error = %v", err)
		}
	}
	if identityCalls != 0 {
		t.Fatalf("identity resolver calls = %d; want 0", identityCalls)
	}
	if files.opens != 0 {
		t.Fatalf("filesystem OpenRoot calls = %d; want 0", files.opens)
	}
}

func TestPathsLocalRejectsNilContext(t *testing.T) {
	root := t.TempDir()
	identityCalls := 0
	preparer, err := NewWithPaths(IdentityResolverFunc(func(context.Context) (Identity, error) {
		identityCalls++
		return testIdentity(), nil
	}), &testFileSystem{root: root}, "/access.log", "/error.log")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := preparer.Prepare(nil, logRequest(api.LogTargetFile, api.LogTargetJournal)); !errors.Is(err, ErrPreparation) {
		t.Fatalf("Prepare(nil) error = %v; want ErrPreparation", err)
	}
	if identityCalls != 0 {
		t.Fatalf("identity resolver calls = %d; want 0", identityCalls)
	}
}

func TestPathsLocalCustomOnlyLeavesStandardPathsUntouched(t *testing.T) {
	root := t.TempDir()
	const custom = "/custom-logs/access.log"
	parent := pathsLocalHostPath(root, "/custom-logs")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	preparer := newPathsLocalPreparer(t, &testFileSystem{root: root}, testIdentity(),
		custom, ErrorLogPath)

	snapshot, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	defer snapshot.Commit()

	assertMetadata(t, pathsLocalHostPath(root, custom), testIdentity(), fileMode)
	assertAbsent(t, filepath.Join(root, ManagedDirectory))
	assertMode(t, parent, 0755)
}

func TestPathsLocalRestoresExistingCustomFile(t *testing.T) {
	root := t.TempDir()
	const custom = "/custom-logs/access.log"
	parent := pathsLocalHostPath(root, "/custom-logs")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	customHost := pathsLocalHostPath(root, custom)
	writeExisting(t, customHost, "preserved content\n", 0600)
	previousFile := readMetadataAt(t, customHost)
	previousParent := readMetadataAt(t, parent)

	preparer := newPathsLocalPreparer(t, &testFileSystem{root: root}, testIdentity(),
		custom, "/custom-logs/unused.log")
	snapshot, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
	if err != nil {
		t.Fatal(err)
	}
	assertContent(t, customHost, "preserved content\n")
	assertMetadata(t, customHost, testIdentity(), fileMode)
	assertMode(t, parent, 0755)

	if err := snapshot.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	assertContent(t, customHost, "preserved content\n")
	assertMode(t, customHost, previousFile.mode)
	assertOwner(t, customHost, previousFile.uid, previousFile.gid)
	assertMode(t, parent, previousParent.mode)
	assertOwner(t, parent, previousParent.uid, previousParent.gid)
}

func TestPathsLocalRollsBackNewFileAfterPartialFailure(t *testing.T) {
	root := t.TempDir()
	firstParent := pathsLocalHostPath(root, "/first")
	secondParent := pathsLocalHostPath(root, "/second")
	for _, parent := range []string{firstParent, secondParent} {
		if err := os.Mkdir(parent, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0755); err != nil {
			t.Fatal(err)
		}
	}
	const first = "/first/one.log"
	const second = "/second/two.log"
	files := &testFileSystem{
		root: root,
		fail: map[string]error{"open:two.log": errors.New("fixture failure")},
	}
	preparer := newPathsLocalPreparer(t, files, testIdentity(), first, second)

	_, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetFile))
	if !errors.Is(err, ErrPreparation) || errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("Prepare() error = %v; want successfully rolled-back ErrPreparation", err)
	}
	assertAbsent(t, pathsLocalHostPath(root, first))
	assertAbsent(t, pathsLocalHostPath(root, second))
	assertMode(t, firstParent, 0755)
	assertMode(t, secondParent, 0755)
}

func TestPathsLocalRollsBackStandardWhenCustomFails(t *testing.T) {
	root := t.TempDir()
	makeLogParent(t, root)
	customParent := pathsLocalHostPath(root, "/custom-logs")
	if err := os.Mkdir(customParent, 0755); err != nil {
		t.Fatal(err)
	}
	const custom = "/custom-logs/fail.log"
	files := &testFileSystem{
		root: root,
		fail: map[string]error{"open:fail.log": errors.New("fixture failure")},
	}
	preparer := newPathsLocalPreparer(t, files, testIdentity(), AccessLogPath, custom)

	_, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetFile))
	if !errors.Is(err, ErrPreparation) || errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("Prepare() error = %v; want successfully rolled-back ErrPreparation", err)
	}
	assertAbsent(t, filepath.Join(root, AccessLogPath))
	assertAbsent(t, filepath.Join(root, ManagedDirectory))
	assertAbsent(t, pathsLocalHostPath(root, custom))
}

func TestPathsLocalRejectsCustomSymlinkParentsAndFiles(t *testing.T) {
	t.Run("parent", func(t *testing.T) {
		root := t.TempDir()
		target := t.TempDir()
		link := pathsLocalHostPath(root, "/linked")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		const custom = "/linked/access.log"
		preparer := newPathsLocalPreparer(t, &testFileSystem{root: root}, testIdentity(),
			custom, "/unused.log")

		_, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
		if !errors.Is(err, ErrUnsafeFilesystem) {
			t.Fatalf("Prepare() error = %v; want ErrUnsafeFilesystem", err)
		}
		assertAbsent(t, filepath.Join(target, "access.log"))
	})

	t.Run("file", func(t *testing.T) {
		root := t.TempDir()
		parent := pathsLocalHostPath(root, "/custom-logs")
		if err := os.Mkdir(parent, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0755); err != nil {
			t.Fatal(err)
		}
		external := filepath.Join(t.TempDir(), "external.log")
		writeExisting(t, external, "must not change\n", 0600)
		if err := os.Symlink(external, filepath.Join(parent, "access.log")); err != nil {
			t.Fatal(err)
		}
		const custom = "/custom-logs/access.log"
		preparer := newPathsLocalPreparer(t, &testFileSystem{root: root}, testIdentity(),
			custom, "/custom-logs/unused.log")

		_, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
		if !errors.Is(err, ErrUnsafeFilesystem) {
			t.Fatalf("Prepare() error = %v; want ErrUnsafeFilesystem", err)
		}
		assertContent(t, external, "must not change\n")
		assertMode(t, external, 0600)
		assertMode(t, parent, 0755)
	})
}

func TestPathsLocalCreatesAndRollsBackMissingDirectories(t *testing.T) {
	root := t.TempDir()
	preparer := newPathsLocalPreparer(t, &testFileSystem{root: root}, testIdentity(), "/missing/nested/access.log", "/unused.log")
	snapshot, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
	if err != nil {
		t.Fatal(err)
	}
	assertMetadata(t, filepath.Join(root, "missing/nested"), testIdentity(), directoryMode)
	assertMetadata(t, filepath.Join(root, "missing/nested/access.log"), testIdentity(), fileMode)
	if err := snapshot.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, filepath.Join(root, "missing"))
}

func TestPathsLocalDeduplicatesSameCustomPath(t *testing.T) {
	root := t.TempDir()
	const custom = "/custom-logs/shared.log"
	parent := pathsLocalHostPath(root, "/custom-logs")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	files := &pathsLocalCountingFileSystem{
		FileSystem: &testFileSystem{root: root},
	}
	preparer := newPathsLocalPreparer(t, files, testIdentity(), custom, custom)

	snapshot, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetFile))
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	defer snapshot.Commit()
	if files.opens != 1 {
		t.Fatalf("filesystem OpenRoot calls = %d; want 1 for deduplicated path", files.opens)
	}
	assertMetadata(t, pathsLocalHostPath(root, custom), testIdentity(), fileMode)
}

func TestPathsLocalDoesNotChangeCustomParentMetadata(t *testing.T) {
	root := t.TempDir()
	first := pathsLocalHostPath(root, "/one")
	second := pathsLocalHostPath(root, "/one/two")
	if err := os.MkdirAll(second, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(first, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(second, 0750); err != nil {
		t.Fatal(err)
	}
	previousRoot := readMetadataAt(t, root)
	previousFirst := readMetadataAt(t, first)
	previousSecond := readMetadataAt(t, second)
	const custom = "/one/two/access.log"
	preparer := newPathsLocalPreparer(t, &testFileSystem{root: root}, testIdentity(),
		custom, "/one/two/unused.log")

	snapshot, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if err := snapshot.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	assertMetadata(t, pathsLocalHostPath(root, custom), testIdentity(), fileMode)
	for name, check := range map[string]struct {
		path     string
		previous metadata
	}{
		"root":   {path: root, previous: previousRoot},
		"first":  {path: first, previous: previousFirst},
		"second": {path: second, previous: previousSecond},
	} {
		if info, err := os.Stat(check.path); err != nil || info.Mode().Perm() != check.previous.mode.Perm() {
			t.Errorf("%s parent mode changed after custom preparation", name)
		}
		assertOwner(t, check.path, check.previous.uid, check.previous.gid)
	}
}

func TestPathsLocalMapsStandardPathFromConfiguration(t *testing.T) {
	root := t.TempDir()
	makeLogParent(t, root)
	const custom = "/custom.log"
	preparer := newPathsLocalPreparer(t, &testFileSystem{root: root}, testIdentity(),
		ErrorLogPath, custom)

	snapshot, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	defer snapshot.Commit()

	assertRegularExists(t, filepath.Join(root, ErrorLogPath))
	assertAbsent(t, filepath.Join(root, AccessLogPath))
	assertAbsent(t, pathsLocalHostPath(root, custom))
}

func newPathsLocalPreparer(t *testing.T, files FileSystem, identity Identity, accessPath, errorPath string) *PathPreparer {
	t.Helper()
	preparer, err := NewWithPaths(IdentityResolverFunc(func(context.Context) (Identity, error) {
		return identity, nil
	}), files, accessPath, errorPath)
	if err != nil {
		t.Fatal(err)
	}
	return preparer
}

func pathsLocalHostPath(root, value string) string {
	return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(value, "/")))
}

type pathsLocalCountingFileSystem struct {
	FileSystem
	opens int
}

func (files *pathsLocalCountingFileSystem) OpenRoot() (Directory, error) {
	files.opens++
	return files.FileSystem.OpenRoot()
}

func TestPathsRejectsUntraversableAncestorWithoutChangingPermissions(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "private")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	identity := Identity{UID: 21001, GID: 21002}
	// The host-test filesystem still opens as the test process; traversal is
	// evaluated against the service identity, not helper's privileged identity.
	preparer := newPathsLocalPreparer(t, &testFileSystem{root: root}, identity, "/private/access.log", "/unused.log")
	_, err := preparer.Prepare(context.Background(), logRequest(api.LogTargetFile, api.LogTargetJournal))
	if !errors.Is(err, ErrPathAccess) {
		t.Fatalf("inaccessible ancestor accepted: %v", err)
	}
	assertMode(t, parent, 0700)
	assertAbsent(t, filepath.Join(parent, "access.log"))
}

func TestPathsDirectoryAccessUsesSelectedIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := readMetadata(info)
	if err != nil {
		t.Fatal(err)
	}
	if !directoryTraversable(info, Identity{UID: meta.uid, GID: meta.gid}) {
		t.Fatal("owner execute ignored")
	}
	if directoryTraversable(info, Identity{UID: meta.uid + 1, GID: meta.gid + 1}) {
		t.Fatal("private directory allowed for other identity")
	}
}
