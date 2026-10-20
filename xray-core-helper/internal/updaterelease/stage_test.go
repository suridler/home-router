package updaterelease

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

const testVersion = "helper-v1.2.3-beta.1"

type fixtureFile struct {
	data []byte
	mode int64
}

type fixtureEntry struct {
	name     string
	linkname string
	typeflag byte
	data     []byte
	mode     int64
}

type archiveFixture struct {
	version      string
	arch         string
	binaries     map[string][]byte
	binaryModes  map[string]int64
	files        map[string]fixtureFile
	dirs         []string
	manifest     map[string]any
	manifestRaw  []byte
	extras       []fixtureEntry
	omitManifest bool
}

func newArchiveFixture(version, arch string) *archiveFixture {
	binaries := make(map[string][]byte)
	modes := make(map[string]int64)
	for index, name := range requiredBinaries {
		content := elfForArch(arch)
		content = append(content, []byte(fmt.Sprintf("binary-%d", index))...)
		binaries[name] = content
		modes[name] = 0o755
	}
	return &archiveFixture{
		version:     version,
		arch:        arch,
		binaries:    binaries,
		binaryModes: modes,
		files: map[string]fixtureFile{
			"README.md": {data: []byte("documentation\n"), mode: 0o644},
			"xray-core-helper/packaging/install-bundle.sh": {
				data: []byte("#!/bin/sh\nexit 0\n"),
				mode: 0o755,
			},
			"xrayui-web/package.json": {data: []byte("{}\n"), mode: 0o644},
		},
		dirs: []string{
			"xray-core-helper",
			"xray-core-helper/bin",
			"xray-core-helper/packaging",
			"xrayui-web",
		},
	}
}

func elfForArch(arch string) []byte {
	machine := uint16(linuxAMD64ELFMachine)
	if arch == "arm64" {
		machine = linuxARM64ELFMachine
	}
	content := make([]byte, 128)
	copy(content, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(content[16:18], 2)
	binary.LittleEndian.PutUint16(content[18:20], machine)
	for index := 64; index < len(content); index++ {
		content[index] = byte(index)
	}
	return content
}

func (fixture *archiveFixture) defaultManifest() map[string]any {
	binaries := make(map[string]string, len(fixture.binaries))
	for name, content := range fixture.binaries {
		digest := sha256.Sum256(content)
		binaries[name] = hex.EncodeToString(digest[:])
	}
	return map[string]any{
		"schemaVersion": 1,
		"version":       fixture.version,
		"commit":        strings.Repeat("a", 40),
		"os":            "linux",
		"arch":          fixture.arch,
		"binaries":      binaries,
		"dependencies": map[string]any{
			"domain-list-community": map[string]any{
				"version": "v0.0.0-20990101000000-deadbeef",
				"module":  "github.com/example/domain-list-community",
			},
		},
	}
}

func (fixture *archiveFixture) build(t *testing.T) []byte {
	t.Helper()
	manifest := fixture.manifest
	if manifest == nil {
		manifest = fixture.defaultManifest()
	}
	manifestBytes := fixture.manifestRaw
	if manifestBytes == nil {
		encoded, err := json.Marshal(manifest)
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		manifestBytes = encoded
	}

	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	for _, directory := range fixture.dirs {
		addTarEntry(t, writer, directory, 0o755, tar.TypeDir, nil, "")
	}
	if !fixture.omitManifest {
		addTarEntry(t, writer, manifestName, 0o644, tar.TypeReg, manifestBytes, "")
	}
	binaryNames := sortedKeys(fixture.binaries)
	for _, name := range binaryNames {
		mode := fixture.binaryModes[name]
		if mode == 0 {
			mode = 0o755
		}
		addTarEntry(t, writer, requiredBinaryPath+name, mode, tar.TypeReg, fixture.binaries[name], "")
	}
	fileNames := sortedKeys(fixture.files)
	for _, name := range fileNames {
		file := fixture.files[name]
		addTarEntry(t, writer, name, file.mode, tar.TypeReg, file.data, "")
	}
	for _, entry := range fixture.extras {
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		addTarEntry(t, writer, entry.name, mode, entry.typeflag, entry.data, entry.linkname)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buffer.Bytes()
}

func addTarEntry(t *testing.T, writer *tar.Writer, name string, mode int64, typeflag byte, data []byte, linkname string) {
	t.Helper()
	header := &tar.Header{
		Name:     name,
		Mode:     mode,
		Typeflag: typeflag,
		Linkname: linkname,
	}
	if typeflag == tar.TypeReg || typeflag == tar.TypeRegA {
		header.Size = int64(len(data))
	}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatalf("write tar header %q: %v", name, err)
	}
	if header.Size > 0 {
		if _, err := writer.Write(data); err != nil {
			t.Fatalf("write tar content %q: %v", name, err)
		}
	}
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func checksumLine(content []byte, name string) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]) + "  " + name + "\n"
}

func stageRelease(version, arch string) Release {
	archive, cli, _ := assetNames(version, arch)
	return Release{Version: version, ArchiveName: archive, CLIName: cli}
}

func newStageServer(t *testing.T, release *Release, archive, checksums []byte) (*httptest.Server, *Client) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		switch request.URL.Path {
		case "/archive":
			_, _ = writer.Write(archive)
		case "/SHA256SUMS":
			_, _ = writer.Write(checksums)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	release.ArchiveURL = server.URL + "/archive"
	release.ChecksumsURL = server.URL + "/SHA256SUMS"
	client := &Client{HTTP: server.Client(), APIBase: server.URL}
	return server, client
}

func TestStageExtractsVerifiedBundle(t *testing.T) {
	t.Parallel()
	arch := "amd64"
	fixture := newArchiveFixture(testVersion, arch)
	fixture.manifest = fixture.defaultManifest()
	fixture.manifest["minimum_installable_version"] = "helper-v1.0.0"
	archive := fixture.build(t)
	release := stageRelease(testVersion, arch)
	_, client := newStageServer(t, &release, archive, []byte(checksumLine(archive, release.ArchiveName)))

	dest := filepath.Join(t.TempDir(), "nested", "bundle")
	bundle, err := Stage(context.Background(), client, release, dest)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Root != dest || bundle.Version != testVersion || bundle.Commit != strings.Repeat("a", 40) ||
		bundle.Arch != arch || bundle.MinimumInstallableVersion != "helper-v1.0.0" {
		t.Fatalf("bundle = %#v", bundle)
	}
	for name, content := range fixture.binaries {
		path := filepath.Join(dest, filepath.FromSlash(requiredBinaryPath+name))
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("binary %s content differs", name)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("binary %s mode = %04o", name, info.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatal(err)
	}
	// The fixture uses a dependency pin unrelated to this repository. Staging
	// must not impose a fixed domain-list-community PIN.
	if _, err := os.Stat(filepath.Join(dest, manifestName)); err != nil {
		t.Fatal(err)
	}
}

func TestStageSupportsBothArchitectures(t *testing.T) {
	t.Parallel()
	for _, arch := range []string{"amd64", "arm64"} {
		arch := arch
		t.Run(arch, func(t *testing.T) {
			t.Parallel()
			fixture := newArchiveFixture(testVersion, arch)
			archive := fixture.build(t)
			release := stageRelease(testVersion, arch)
			_, client := newStageServer(t, &release, archive, []byte(checksumLine(archive, release.ArchiveName)))
			bundle, err := Stage(context.Background(), client, release, filepath.Join(t.TempDir(), "bundle"))
			if err != nil {
				t.Fatal(err)
			}
			if bundle.Arch != arch {
				t.Fatalf("arch = %q, want %q", bundle.Arch, arch)
			}
			if bundle.MinimumInstallableVersion != "" {
				t.Fatalf("minimum version = %q, want empty", bundle.MinimumInstallableVersion)
			}
		})
	}
}

func TestStageRequiresNewDestination(t *testing.T) {
	t.Parallel()
	fixture := newArchiveFixture(testVersion, "amd64")
	archive := fixture.build(t)
	release := stageRelease(testVersion, "amd64")
	_, client := newStageServer(t, &release, archive, []byte(checksumLine(archive, release.ArchiveName)))

	t.Run("directory", func(t *testing.T) {
		t.Parallel()
		dest := filepath.Join(t.TempDir(), "bundle")
		if err := os.Mkdir(dest, 0o755); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(dest, "marker")
		if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Stage(context.Background(), client, release, dest); !errors.Is(err, ErrDestinationExists) {
			t.Fatalf("error = %v, want ErrDestinationExists", err)
		}
		if content, err := os.ReadFile(marker); err != nil || string(content) != "keep" {
			t.Fatalf("marker = %q, %v", content, err)
		}
	})
	t.Run("file", func(t *testing.T) {
		t.Parallel()
		dest := filepath.Join(t.TempDir(), "bundle")
		if err := os.WriteFile(dest, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Stage(context.Background(), client, release, dest); !errors.Is(err, ErrDestinationExists) {
			t.Fatalf("error = %v, want ErrDestinationExists", err)
		}
		if content, err := os.ReadFile(dest); err != nil || string(content) != "keep" {
			t.Fatalf("destination = %q, %v", content, err)
		}
	})
}

func TestStageRejectsInvalidReleaseBeforeDownload(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), APIBase: server.URL}
	base := Release{
		Version:      testVersion,
		ArchiveName:  "xray-core-helper-v9.9.9-linux-amd64.tar.gz",
		ArchiveURL:   server.URL + "/archive",
		ChecksumsURL: server.URL + "/SHA256SUMS",
	}
	tests := []struct {
		name    string
		client  *Client
		release Release
		want    error
	}{
		{name: "nil client", client: nil, release: base, want: ErrInvalidArgument},
		{name: "invalid version", client: client, release: Release{Version: "1.2.3"}, want: ErrInvalidVersion},
		{name: "archive version mismatch", client: client, release: base, want: ErrInvalidRelease},
		{name: "missing archive URL", client: client, release: Release{
			Version: testVersion, ArchiveName: "xray-core-helper-v1.2.3-beta.1-linux-amd64.tar.gz",
			ChecksumsURL: server.URL + "/SHA256SUMS",
		}, want: ErrInvalidRelease},
		{name: "missing checksums URL", client: client, release: Release{
			Version: testVersion, ArchiveName: "xray-core-helper-v1.2.3-beta.1-linux-amd64.tar.gz",
			ArchiveURL: server.URL + "/archive",
		}, want: ErrInvalidRelease},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "bundle")
			if _, err := Stage(context.Background(), test.client, test.release, dest); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination exists after rejection: %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("server calls = %d, want 0", calls.Load())
	}
}

func TestStageFailureRemovesDestination(t *testing.T) {
	t.Parallel()
	fixture := newArchiveFixture(testVersion, "amd64")
	archive := fixture.build(t)
	release := stageRelease(testVersion, "amd64")
	tests := []struct {
		name      string
		checksums string
		want      error
	}{
		{name: "checksum mismatch", checksums: checksumLine([]byte("different"), release.ArchiveName), want: ErrChecksumMismatch},
		{name: "missing checksum", checksums: "", want: ErrInvalidChecksums},
		{name: "other file only", checksums: checksumLine(archive, "other.tar.gz"), want: ErrInvalidChecksums},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			serverRelease := release
			_, client := newStageServer(t, &serverRelease, archive, []byte(test.checksums))
			dest := filepath.Join(t.TempDir(), "bundle")
			if _, err := Stage(context.Background(), client, serverRelease, dest); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination remains after failure: %v", err)
			}
		})
	}
}

func TestStageDownloadFailureIsRedactedAndCleansUp(t *testing.T) {
	t.Parallel()
	const secret = "SECRET-ARCHIVE-BODY"
	release := stageRelease(testVersion, "amd64")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(secret))
	}))
	defer server.Close()
	release.ArchiveURL = server.URL + "/archive"
	release.ChecksumsURL = server.URL + "/SHA256SUMS"
	client := &Client{HTTP: server.Client(), APIBase: server.URL}
	dest := filepath.Join(t.TempDir(), "bundle")
	_, err := Stage(context.Background(), client, release, dest)
	if !errors.Is(err, ErrDownload) {
		t.Fatalf("error = %v, want ErrDownload", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks response body: %v", err)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination remains after failure: %v", err)
	}
}

func TestParseChecksums(t *testing.T) {
	t.Parallel()
	const target = "xray-core-helper-v1.2.3-linux-amd64.tar.gz"
	digest := strings.Repeat("ab", 32)
	other := strings.Repeat("cd", 32)
	tests := []struct {
		name    string
		content string
		want    string
		wantErr error
	}{
		{name: "exact", content: digest + "  " + target + "\n", want: digest},
		{name: "without final newline", content: digest + "  " + target, want: digest},
		{name: "extra valid entry", content: other + "  cli\n" + digest + "  " + target + "\n", want: digest},
		{name: "empty", content: "", wantErr: ErrInvalidChecksums},
		{name: "blank line", content: digest + "  " + target + "\n\n", wantErr: ErrInvalidChecksums},
		{name: "missing target", content: other + "  cli\n", wantErr: ErrInvalidChecksums},
		{name: "duplicate target", content: digest + "  " + target + "\n" + digest + "  " + target + "\n", wantErr: ErrInvalidChecksums},
		{name: "uppercase digest", content: strings.ToUpper(digest) + "  " + target + "\n", wantErr: ErrInvalidChecksums},
		{name: "short digest", content: digest[:63] + "  " + target + "\n", wantErr: ErrInvalidChecksums},
		{name: "one separator", content: digest + " " + target + "\n", wantErr: ErrInvalidChecksums},
		{name: "three separators", content: digest + "   " + target + "\n", wantErr: ErrInvalidChecksums},
		{name: "path filename", content: digest + "  ../" + target + "\n", wantErr: ErrInvalidChecksums},
		{name: "malformed extra", content: digest + "  " + target + "\nnot-a-checksum\n", wantErr: ErrInvalidChecksums},
		{name: "crlf", content: digest + "  " + target + "\r\n", wantErr: ErrInvalidChecksums},
		{name: "nul byte", content: digest + "  " + target + "\x00\n", wantErr: ErrInvalidChecksums},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseChecksums([]byte(test.content), target)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(got[:]) != test.want {
				t.Fatalf("digest = %s, want %s", hex.EncodeToString(got[:]), test.want)
			}
		})
	}
}

func TestStageRejectsUnsafeArchives(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*archiveFixture)
	}{
		{name: "parent traversal", mutate: func(fixture *archiveFixture) {
			fixture.extras = append(fixture.extras, fixtureEntry{name: "../escape", typeflag: tar.TypeReg, data: []byte("x")})
		}},
		{name: "nested traversal", mutate: func(fixture *archiveFixture) {
			fixture.extras = append(fixture.extras, fixtureEntry{name: "xray-core-helper/../../escape", typeflag: tar.TypeReg, data: []byte("x")})
		}},
		{name: "absolute path", mutate: func(fixture *archiveFixture) {
			fixture.extras = append(fixture.extras, fixtureEntry{name: "/tmp/escape", typeflag: tar.TypeReg, data: []byte("x")})
		}},
		{name: "symlink", mutate: func(fixture *archiveFixture) {
			fixture.extras = append(fixture.extras, fixtureEntry{
				name: "xray-core-helper/link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd",
			})
		}},
		{name: "hardlink", mutate: func(fixture *archiveFixture) {
			fixture.extras = append(fixture.extras, fixtureEntry{
				name: "xray-core-helper/hardlink", typeflag: tar.TypeLink, linkname: "README.md",
			})
		}},
		{name: "fifo", mutate: func(fixture *archiveFixture) {
			fixture.extras = append(fixture.extras, fixtureEntry{name: "xray-core-helper/fifo", typeflag: tar.TypeFifo})
		}},
		{name: "character device", mutate: func(fixture *archiveFixture) {
			fixture.extras = append(fixture.extras, fixtureEntry{name: "xray-core-helper/device", typeflag: tar.TypeChar})
		}},
		{name: "dot segment", mutate: func(fixture *archiveFixture) {
			fixture.extras = append(fixture.extras, fixtureEntry{
				name: "xray-core-helper/./extra", typeflag: tar.TypeReg, data: []byte("x"),
			})
		}},
		{name: "empty segment", mutate: func(fixture *archiveFixture) {
			fixture.extras = append(fixture.extras, fixtureEntry{
				name: "xray-core-helper//extra", typeflag: tar.TypeReg, data: []byte("x"),
			})
		}},
		{name: "backslash", mutate: func(fixture *archiveFixture) {
			fixture.extras = append(fixture.extras, fixtureEntry{
				name: `xray-core-helper\extra`, typeflag: tar.TypeReg, data: []byte("x"),
			})
		}},
		{name: "duplicate", mutate: func(fixture *archiveFixture) {
			fixture.extras = append(fixture.extras, fixtureEntry{
				name: "README.md", typeflag: tar.TypeReg, data: []byte("replacement"),
			})
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newArchiveFixture(testVersion, "amd64")
			test.mutate(fixture)
			archive := fixture.build(t)
			release := stageRelease(testVersion, "amd64")
			_, client := newStageServer(t, &release, archive, []byte(checksumLine(archive, release.ArchiveName)))
			dest := filepath.Join(t.TempDir(), "bundle")
			if _, err := Stage(context.Background(), client, release, dest); !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("error = %v, want ErrUnsafeArchive", err)
			}
			if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination remains after failure: %v", err)
			}
		})
	}
}

func TestStageRejectsExcessiveEntryCount(t *testing.T) {
	t.Parallel()
	fixture := newArchiveFixture(testVersion, "amd64")
	for index := 0; index <= maxArchiveEntries; index++ {
		fixture.extras = append(fixture.extras, fixtureEntry{
			name:     fmt.Sprintf("many/%05d", index),
			typeflag: tar.TypeReg,
		})
	}
	archive := fixture.build(t)
	release := stageRelease(testVersion, "amd64")
	_, client := newStageServer(t, &release, archive, []byte(checksumLine(archive, release.ArchiveName)))
	dest := filepath.Join(t.TempDir(), "bundle")
	if _, err := Stage(context.Background(), client, release, dest); !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("error = %v, want ErrUnsafeArchive", err)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination remains after failure: %v", err)
	}
}

func TestStageRejectsExpandedSizeLimit(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	header := &tar.Header{
		Name:     requiredBinaryPath + "huge",
		Mode:     0o644,
		Size:     maxExpandedSize + 1,
		Typeflag: tar.TypeReg,
	}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buffer.Bytes()
	release := stageRelease(testVersion, "amd64")
	_, client := newStageServer(t, &release, archive, []byte(checksumLine(archive, release.ArchiveName)))
	dest := filepath.Join(t.TempDir(), "bundle")
	if _, err := Stage(context.Background(), client, release, dest); !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("error = %v, want ErrUnsafeArchive", err)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination remains after failure: %v", err)
	}
}

func TestStageRejectsMalformedArchives(t *testing.T) {
	t.Parallel()
	fixture := newArchiveFixture(testVersion, "amd64")
	valid := fixture.build(t)
	tests := []struct {
		name    string
		archive []byte
		want    error
	}{
		{name: "not gzip", archive: []byte("not a gzip archive"), want: ErrInvalidArchive},
		{name: "truncated", archive: valid[:len(valid)/2], want: ErrInvalidArchive},
		{name: "empty", archive: nil, want: ErrInvalidArchive},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			release := stageRelease(testVersion, "amd64")
			_, client := newStageServer(t, &release, test.archive, []byte(checksumLine(test.archive, release.ArchiveName)))
			dest := filepath.Join(t.TempDir(), "bundle")
			if _, err := Stage(context.Background(), client, release, dest); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination remains after failure: %v", err)
			}
		})
	}
}

func TestStageValidatesManifest(t *testing.T) {
	t.Parallel()
	otherArch := "arm64"
	tests := []struct {
		name    string
		mutate  func(*archiveFixture)
		wantErr error
	}{
		{name: "schema", mutate: func(fixture *archiveFixture) {
			manifest := fixture.defaultManifest()
			manifest["schemaVersion"] = 2
			fixture.manifest = manifest
		}, wantErr: ErrInvalidManifest},
		{name: "version", mutate: func(fixture *archiveFixture) {
			manifest := fixture.defaultManifest()
			manifest["version"] = "helper-v9.9.9"
			fixture.manifest = manifest
		}, wantErr: ErrInvalidManifest},
		{name: "os", mutate: func(fixture *archiveFixture) {
			manifest := fixture.defaultManifest()
			manifest["os"] = "darwin"
			fixture.manifest = manifest
		}, wantErr: ErrInvalidManifest},
		{name: "arch", mutate: func(fixture *archiveFixture) {
			manifest := fixture.defaultManifest()
			manifest["arch"] = otherArch
			fixture.manifest = manifest
		}, wantErr: ErrInvalidManifest},
		{name: "uppercase commit", mutate: func(fixture *archiveFixture) {
			manifest := fixture.defaultManifest()
			manifest["commit"] = strings.Repeat("A", 40)
			fixture.manifest = manifest
		}, wantErr: ErrInvalidManifest},
		{name: "short commit", mutate: func(fixture *archiveFixture) {
			manifest := fixture.defaultManifest()
			manifest["commit"] = strings.Repeat("a", 39)
			fixture.manifest = manifest
		}, wantErr: ErrInvalidManifest},
		{name: "nil binaries", mutate: func(fixture *archiveFixture) {
			manifest := fixture.defaultManifest()
			manifest["binaries"] = nil
			fixture.manifest = manifest
		}, wantErr: ErrInvalidManifest},
		{name: "missing binary entry", mutate: func(fixture *archiveFixture) {
			manifest := fixture.defaultManifest()
			delete(manifest["binaries"].(map[string]string), "domain-list-community")
			fixture.manifest = manifest
		}, wantErr: ErrInvalidManifest},
		{name: "invalid binary digest", mutate: func(fixture *archiveFixture) {
			manifest := fixture.defaultManifest()
			manifest["binaries"].(map[string]string)["domain-list-community"] = "bad"
			fixture.manifest = manifest
		}, wantErr: ErrInvalidManifest},
		{name: "invalid extra binary digest", mutate: func(fixture *archiveFixture) {
			manifest := fixture.defaultManifest()
			manifest["binaries"].(map[string]string)["extra"] = "bad"
			fixture.manifest = manifest
		}, wantErr: ErrInvalidManifest},
		{name: "invalid minimum version", mutate: func(fixture *archiveFixture) {
			manifest := fixture.defaultManifest()
			manifest["minimum_installable_version"] = "1.2.3"
			fixture.manifest = manifest
		}, wantErr: ErrInvalidManifest},
		{name: "future minimum version", mutate: func(fixture *archiveFixture) {
			manifest := fixture.defaultManifest()
			manifest["minimum_installable_version"] = "helper-v9.9.9"
			fixture.manifest = manifest
		}, wantErr: ErrInvalidManifest},
		{name: "unknown field", mutate: func(fixture *archiveFixture) {
			encoded, err := json.Marshal(fixture.defaultManifest())
			if err != nil {
				t.Fatal(err)
			}
			manifest := make(map[string]any)
			if err := json.Unmarshal(encoded, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["unknownField"] = true
			fixture.manifest = manifest
		}, wantErr: ErrInvalidManifest},
		{name: "missing manifest", mutate: func(fixture *archiveFixture) {
			fixture.omitManifest = true
		}, wantErr: ErrInvalidManifest},
		{name: "missing binary file", mutate: func(fixture *archiveFixture) {
			fixture.manifest = fixture.defaultManifest()
			delete(fixture.binaries, "xray-core-helperd")
		}, wantErr: ErrInvalidManifest},
		{name: "non ELF binary", mutate: func(fixture *archiveFixture) {
			fixture.binaries["xray-core-helper"] = []byte("not an ELF binary")
		}, wantErr: ErrInvalidManifest},
		{name: "wrong ELF architecture", mutate: func(fixture *archiveFixture) {
			fixture.binaries["xray-core-helper"] = elfForArch(otherArch)
		}, wantErr: ErrInvalidManifest},
		{name: "non executable binary", mutate: func(fixture *archiveFixture) {
			fixture.binaryModes["xray-core-helper"] = 0o644
		}, wantErr: ErrInvalidManifest},
		{name: "binary digest mismatch", mutate: func(fixture *archiveFixture) {
			manifest := fixture.defaultManifest()
			manifest["binaries"].(map[string]string)["xray-core-helper"] = strings.Repeat("0", 64)
			fixture.manifest = manifest
		}, wantErr: ErrChecksumMismatch},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newArchiveFixture(testVersion, "amd64")
			test.mutate(fixture)
			archive := fixture.build(t)
			release := stageRelease(testVersion, "amd64")
			_, client := newStageServer(t, &release, archive, []byte(checksumLine(archive, release.ArchiveName)))
			dest := filepath.Join(t.TempDir(), "bundle")
			if _, err := Stage(context.Background(), client, release, dest); !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination remains after failure: %v", err)
			}
		})
	}
}

func TestStageRejectsManifestTrailingData(t *testing.T) {
	t.Parallel()
	fixture := newArchiveFixture(testVersion, "amd64")
	encoded, err := json.Marshal(fixture.defaultManifest())
	if err != nil {
		t.Fatal(err)
	}
	fixture.manifestRaw = append(encoded, []byte(` {}`)...)
	archive := fixture.build(t)
	release := stageRelease(testVersion, "amd64")
	_, client := newStageServer(t, &release, archive, []byte(checksumLine(archive, release.ArchiveName)))
	dest := filepath.Join(t.TempDir(), "bundle")
	if _, err := Stage(context.Background(), client, release, dest); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("error = %v, want ErrInvalidManifest", err)
	}
}

func TestExtractArchiveHonorsCancellation(t *testing.T) {
	t.Parallel()
	fixture := newArchiveFixture(testVersion, "amd64")
	archive := fixture.build(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := extractArchive(ctx, archive, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestStageRejectsOversizedManifest(t *testing.T) {
	t.Parallel()
	fixture := newArchiveFixture(testVersion, "amd64")
	fixture.manifestRaw = []byte(`{"schemaVersion":1,"padding":"` + strings.Repeat("x", maxManifestSize) + `"}`)
	archive := fixture.build(t)
	release := stageRelease(testVersion, "amd64")
	_, client := newStageServer(t, &release, archive, []byte(checksumLine(archive, release.ArchiveName)))
	dest := filepath.Join(t.TempDir(), "bundle")
	if _, err := Stage(context.Background(), client, release, dest); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("error = %v, want ErrInvalidManifest", err)
	}
}
