package updaterelease

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxChecksumsSize     = 1 << 20
	maxArchiveSize       = int64(512) << 20
	maxExpandedSize      = int64(2) << 30
	maxArchiveEntries    = 10000
	maxManifestSize      = 1 << 20
	manifestName         = "release.json"
	requiredBinaryPath   = "xray-core-helper/bin/"
	linuxAMD64ELFMachine = 62
	linuxARM64ELFMachine = 183
)

var (
	// ErrInvalidArchive reports a malformed gzip or tar stream.
	ErrInvalidArchive = errors.New("invalid release archive")
	// ErrInvalidChecksums reports malformed or ambiguous SHA256SUMS content.
	ErrInvalidChecksums = errors.New("invalid release checksums")
	// ErrChecksumMismatch reports an archive or binary digest mismatch.
	ErrChecksumMismatch = errors.New("release checksum mismatch")
	// ErrUnsafeArchive reports links, traversal, duplicates or archive limits.
	ErrUnsafeArchive = errors.New("unsafe release archive")
	// ErrInvalidManifest reports unusable release.json metadata.
	ErrInvalidManifest = errors.New("invalid release manifest")
	// ErrDestinationExists reports a staging directory that is not new.
	ErrDestinationExists = errors.New("staging destination already exists")
)

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

var requiredBinaries = []string{
	"xray-core-helper",
	"xray-core-helperd",
	"domain-list-community",
}

// Bundle is a fully verified staging directory that has not been installed or
// executed.
type Bundle struct {
	Root                      string
	Version                   string
	Commit                    string
	Arch                      string
	MinimumInstallableVersion string
}

type releaseManifest struct {
	SchemaVersion             int               `json:"schemaVersion"`
	Version                   string            `json:"version"`
	Commit                    string            `json:"commit"`
	OS                        string            `json:"os"`
	Arch                      string            `json:"arch"`
	Binaries                  map[string]string `json:"binaries"`
	Dependencies              json.RawMessage   `json:"dependencies,omitempty"`
	MinimumInstallableVersion string            `json:"minimum_installable_version,omitempty"`
}

// Stage downloads the checksums and archive for release into a new dest
// directory, verifies them and returns the verified bundle. It never runs a
// candidate binary, npm or an installer. On failure dest is removed.
func Stage(ctx context.Context, client *Client, release Release, dest string) (bundle Bundle, err error) {
	if client == nil || ctx == nil || dest == "" {
		return Bundle{}, ErrInvalidArgument
	}
	if err := ValidateVersion(release.Version); err != nil {
		return Bundle{}, err
	}
	arch, err := architectureFromArchive(release.Version, release.ArchiveName)
	if err != nil {
		return Bundle{}, err
	}
	if release.ArchiveURL == "" || release.ChecksumsURL == "" {
		return Bundle{}, ErrInvalidRelease
	}
	if _, statErr := os.Lstat(dest); statErr == nil {
		return Bundle{}, ErrDestinationExists
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return Bundle{}, statErr
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return Bundle{}, err
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Bundle{}, ErrDestinationExists
		}
		return Bundle{}, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(dest))
		}
	}()

	checksums, err := client.Download(ctx, release.ChecksumsURL, maxChecksumsSize)
	if err != nil {
		return Bundle{}, err
	}
	expected, err := parseChecksums(checksums, release.ArchiveName)
	if err != nil {
		return Bundle{}, err
	}
	archive, err := client.Download(ctx, release.ArchiveURL, maxArchiveSize)
	if err != nil {
		return Bundle{}, err
	}
	actual := sha256.Sum256(archive)
	if subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
		return Bundle{}, ErrChecksumMismatch
	}
	if err := extractArchive(ctx, archive, dest); err != nil {
		return Bundle{}, err
	}
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	manifest, err := readManifest(dest)
	if err != nil {
		return Bundle{}, err
	}
	if err := validateManifest(manifest, release.Version, arch); err != nil {
		return Bundle{}, err
	}
	for _, name := range requiredBinaries {
		if err := validateBinary(dest, arch, name, manifest.Binaries[name]); err != nil {
			return Bundle{}, err
		}
	}
	return Bundle{
		Root:                      dest,
		Version:                   release.Version,
		Commit:                    manifest.Commit,
		Arch:                      arch,
		MinimumInstallableVersion: manifest.MinimumInstallableVersion,
	}, nil
}

func architectureFromArchive(version, archiveName string) (string, error) {
	for _, arch := range []string{"amd64", "arm64"} {
		expected, _, _ := assetNames(version, arch)
		if archiveName == expected {
			return arch, nil
		}
	}
	return "", ErrInvalidRelease
}

func parseChecksums(content []byte, target string) ([sha256.Size]byte, error) {
	var expected [sha256.Size]byte
	if len(content) == 0 || len(content) > maxChecksumsSize {
		return expected, ErrInvalidChecksums
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return expected, ErrInvalidChecksums
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return expected, ErrInvalidChecksums
	}
	seen := make(map[string]struct{}, len(lines))
	found := false
	for _, line := range lines {
		if len(line) < sha256.Size*2+2+1 || line[sha256.Size*2] != ' ' ||
			line[sha256.Size*2+1] != ' ' {
			return expected, ErrInvalidChecksums
		}
		digest := line[:sha256.Size*2]
		filename := line[sha256.Size*2+2:]
		if !digestPattern.MatchString(digest) || !validChecksumFilename(filename) {
			return expected, ErrInvalidChecksums
		}
		if _, exists := seen[filename]; exists {
			return expected, ErrInvalidChecksums
		}
		seen[filename] = struct{}{}
		if filename != target {
			continue
		}
		if found {
			return expected, ErrInvalidChecksums
		}
		decoded, decodeErr := hex.DecodeString(digest)
		if decodeErr != nil {
			return expected, ErrInvalidChecksums
		}
		copy(expected[:], decoded)
		found = true
	}
	if !found {
		return expected, ErrInvalidChecksums
	}
	return expected, nil
}

func validChecksumFilename(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if character >= '0' && character <= '9' ||
			character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character == '.' || character == '_' || character == '+' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func extractArchive(ctx context.Context, archive []byte, dest string) error {
	compressed, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return ErrInvalidArchive
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	seen := make(map[string]struct{})
	var expanded int64
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ErrInvalidArchive
		}
		entries++
		if entries > maxArchiveEntries {
			return ErrUnsafeArchive
		}
		name, err := safeArchivePath(header.Name)
		if err != nil {
			return err
		}
		if header.Size < 0 {
			return ErrUnsafeArchive
		}
		if _, exists := seen[name]; exists {
			return ErrUnsafeArchive
		}
		seen[name] = struct{}{}
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return ErrUnsafeArchive
			}
			directory := filepath.Join(dest, filepath.FromSlash(name))
			if err := os.MkdirAll(directory, 0o755); err != nil {
				return ErrUnsafeArchive
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size > maxExpandedSize-expanded {
				return ErrUnsafeArchive
			}
			expanded += header.Size
			if err := extractRegularFile(ctx, reader, dest, name, header); err != nil {
				return err
			}
		default:
			return ErrUnsafeArchive
		}
	}
	if len(seen) == 0 {
		return ErrInvalidArchive
	}
	return nil
}

func safeArchivePath(raw string) (string, error) {
	if raw == "" || strings.HasPrefix(raw, "/") ||
		strings.ContainsRune(raw, 0) || strings.Contains(raw, `\`) {
		return "", ErrUnsafeArchive
	}
	trimmed := strings.TrimSuffix(raw, "/")
	if trimmed == "" {
		return "", ErrUnsafeArchive
	}
	for _, part := range strings.Split(trimmed, "/") {
		if part == "" || part == "." || part == ".." {
			return "", ErrUnsafeArchive
		}
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrUnsafeArchive
	}
	return cleaned, nil
}

func extractRegularFile(ctx context.Context, reader io.Reader, dest, name string, header *tar.Header) error {
	target := filepath.Join(dest, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return ErrUnsafeArchive
	}
	mode := os.FileMode(0o644)
	if header.Mode&0o111 != 0 {
		mode = 0o755
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return ErrUnsafeArchive
	}
	written, copyErr := io.CopyN(file, contextReader{ctx: ctx, reader: reader}, header.Size)
	closeErr := file.Close()
	if copyErr != nil {
		if errors.Is(copyErr, context.Canceled) || errors.Is(copyErr, context.DeadlineExceeded) {
			return copyErr
		}
		return ErrInvalidArchive
	}
	if written != header.Size || closeErr != nil {
		return ErrInvalidArchive
	}
	if err := os.Chmod(target, mode); err != nil {
		return ErrUnsafeArchive
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func readManifest(root string) (releaseManifest, error) {
	var manifest releaseManifest
	target := filepath.Join(root, manifestName)
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxManifestSize {
		return manifest, ErrInvalidManifest
	}
	content, err := os.ReadFile(target)
	if err != nil {
		return manifest, ErrInvalidManifest
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, ErrInvalidManifest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return manifest, ErrInvalidManifest
	}
	return manifest, nil
}

func validateManifest(manifest releaseManifest, version, arch string) error {
	if manifest.SchemaVersion != 1 || manifest.Version != version ||
		manifest.OS != "linux" || manifest.Arch != arch ||
		!commitPattern.MatchString(manifest.Commit) {
		return ErrInvalidManifest
	}
	if manifest.Binaries == nil {
		return ErrInvalidManifest
	}
	for name, digest := range manifest.Binaries {
		if name == "" || !digestPattern.MatchString(digest) {
			return ErrInvalidManifest
		}
	}
	for _, name := range requiredBinaries {
		if _, exists := manifest.Binaries[name]; !exists {
			return ErrInvalidManifest
		}
	}
	if manifest.MinimumInstallableVersion != "" {
		if err := ValidateVersion(manifest.MinimumInstallableVersion); err != nil {
			return ErrInvalidManifest
		}
		comparison, err := Compare(manifest.MinimumInstallableVersion, version)
		if err != nil || comparison > 0 {
			return ErrInvalidManifest
		}
	}
	return nil
}

func validateBinary(root, arch, name, digest string) error {
	if !digestPattern.MatchString(digest) {
		return ErrInvalidManifest
	}
	expected, err := hex.DecodeString(digest)
	if err != nil {
		return ErrInvalidManifest
	}
	target := filepath.Join(root, filepath.FromSlash(requiredBinaryPath+name))
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return ErrInvalidManifest
	}
	file, err := os.Open(target)
	if err != nil {
		return ErrInvalidManifest
	}
	defer file.Close()

	header := make([]byte, 64)
	if _, err := io.ReadFull(file, header); err != nil {
		return ErrInvalidManifest
	}
	if !validELFHeader(header, arch) {
		return ErrInvalidManifest
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ErrInvalidManifest
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ErrInvalidManifest
	}
	if subtle.ConstantTimeCompare(expected, hash.Sum(nil)) != 1 {
		return ErrChecksumMismatch
	}
	return nil
}

func validELFHeader(header []byte, arch string) bool {
	if len(header) < 20 || header[0] != 0x7f || header[1] != 'E' ||
		header[2] != 'L' || header[3] != 'F' ||
		header[4] != 2 || header[5] != 1 || header[6] != 1 {
		return false
	}
	fileType := binary.LittleEndian.Uint16(header[16:18])
	if fileType != 2 && fileType != 3 {
		return false
	}
	machine := binary.LittleEndian.Uint16(header[18:20])
	switch arch {
	case "amd64":
		return machine == linuxAMD64ELFMachine
	case "arm64":
		return machine == linuxARM64ELFMachine
	default:
		return false
	}
}

// VerifyLocal validates an operator-provided unpacked bundle. Authentication of
// its source is the operator's responsibility, unlike Stage's downloaded archive.
func VerifyLocal(root, arch string) (Bundle, error) {
	manifest, err := readManifest(root)
	if err != nil {
		return Bundle{}, err
	}
	if err = ValidateVersion(manifest.Version); err != nil {
		return Bundle{}, err
	}
	if err = validateManifest(manifest, manifest.Version, arch); err != nil {
		return Bundle{}, err
	}
	for _, name := range requiredBinaries {
		if err = validateBinary(root, arch, name, manifest.Binaries[name]); err != nil {
			return Bundle{}, err
		}
	}
	return Bundle{Root: root, Version: manifest.Version, Commit: manifest.Commit, Arch: arch, MinimumInstallableVersion: manifest.MinimumInstallableVersion}, nil
}
