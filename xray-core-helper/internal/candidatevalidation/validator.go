// Package candidatevalidation validates downloaded geodata against Xray
// without changing the configured asset directory.
package candidatevalidation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/geodatadownload"
	"home-router/xray-core-helper/internal/geodatalimits"
	"home-router/xray-core-helper/internal/xrayconfig"
)

const (
	candidateDirectoryMode = 0700
	candidateAssetMode     = 0600
	customAssetPendingDir  = ".xray-core-helper-consolidation-pending"
)

var (
	ErrInvalidInput          = errors.New("invalid candidate validation input")
	ErrCandidateSetup        = errors.New("candidate asset setup failed")
	ErrMissingCompanionAsset = errors.New("required managed companion asset is missing")
	ErrUnsupportedAsset      = errors.New("unsupported external asset reference")
	ErrXrayValidation        = errors.New("Xray rejected candidate assets")
	ErrCandidateChanged      = errors.New("custom geosite changed during validation")
	ErrValidationCanceled    = errors.New("candidate validation canceled")
	ErrCandidateCleanup      = errors.New("candidate asset cleanup failed")

	companionAssetName = regexp.MustCompile(`^(geosite|geoip)_[A-Z]{2}\.dat$`)
)

// SourceFile is a no-follow opened regular-file candidate source.
type SourceFile interface {
	io.Reader
	Stat() (os.FileInfo, error)
	Close() error
}

// CandidateFile is a newly created file in the private candidate directory.
type CandidateFile interface {
	io.Writer
	Close() error
}

// FileSystem is the bounded filesystem surface used to build and clean a
// private candidate directory.
type FileSystem interface {
	MkdirTemp(string, string) (string, error)
	Chmod(string, os.FileMode) error
	OpenSource(string) (SourceFile, error)
	OpenFile(string, int, os.FileMode) (CandidateFile, error)
	RemoveAll(string) error
}

// CommandRunner executes the configured fixed Xray command with an explicit
// environment.
type CommandRunner interface {
	Run(context.Context, string, []string, []string) error
}

type Options struct {
	FileSystem FileSystem
	Runner     CommandRunner
	TempRoot   string
}

type Validator struct {
	maxAssetSize int
	binary       string
	config       string
	assetDir     string
	userGeosite  string
	userGeoip    string
	tempRoot     string
	files        FileSystem
	runner       CommandRunner
	assetReader  xrayconfig.Reader
}

type osFileSystem struct{}

func (osFileSystem) MkdirTemp(root, pattern string) (string, error) {
	return os.MkdirTemp(root, pattern)
}

func (osFileSystem) Chmod(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}

func (osFileSystem) OpenSource(path string) (SourceFile, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, os.ErrInvalid
	}
	return file, nil
}

func (osFileSystem) OpenFile(path string, flag int, mode os.FileMode) (CandidateFile, error) {
	return os.OpenFile(path, flag, mode)
}

func (osFileSystem) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, executable string, args []string, environment []string) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = environment
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

// New creates a validator for the absolute paths in the validated bootstrap
// configuration. The candidate root defaults to the system temporary directory.
func New(config bootstrap.Config, options Options) (*Validator, error) {
	if err := config.Validate(); err != nil {
		return nil, ErrInvalidInput
	}
	for _, path := range []string{config.Xray.Binary, config.Xray.Config, config.Xray.AssetDir} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, ErrInvalidInput
		}
	}
	if _, err := config.GeodataAssetPath("geosite.dat"); err != nil {
		return nil, ErrInvalidInput
	}

	if options.FileSystem == nil {
		options.FileSystem = osFileSystem{}
	}
	if options.Runner == nil {
		options.Runner = execCommandRunner{}
	}
	if options.TempRoot == "" {
		options.TempRoot = os.TempDir()
	}
	if !filepath.IsAbs(options.TempRoot) || filepath.Clean(options.TempRoot) != options.TempRoot {
		return nil, ErrInvalidInput
	}
	rootPath, err := filepath.EvalSymlinks(options.TempRoot)
	if err != nil {
		return nil, ErrInvalidInput
	}
	if isWithin(config.Xray.AssetDir, rootPath) {
		return nil, ErrInvalidInput
	}

	return &Validator{maxAssetSize: geodatalimits.Bytes(config.Geodata.MaxAssetSizeMB),
		binary:      config.Xray.Binary,
		config:      config.Xray.Config,
		assetDir:    config.Xray.AssetDir,
		userGeosite: config.Geodata.UserGeosite,
		userGeoip:   config.Geodata.UserGeoip,
		tempRoot:    rootPath,
		files:       options.FileSystem,
		runner:      options.Runner,
		assetReader: xrayconfig.NewReader(xrayconfig.Settings{
			Binary:   config.Xray.Binary,
			Config:   config.Xray.Config,
			API:      config.Xray.API,
			AssetDir: config.Xray.AssetDir,
		}),
	}, nil
}

// Validate assembles a short-lived private asset directory from a verified
// download result and any required, allowlisted managed companion assets.
func (validator *Validator) Validate(ctx context.Context, downloaded *geodatadownload.Result) (resultErr error) {
	if ctx == nil || downloaded == nil || !validDownloadResult(downloaded, validator.assetDir) {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrValidationCanceled, err)
	}
	referenced, err := validator.assetReader.ReferencedExternalAssets(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return errors.Join(ErrValidationCanceled, ctx.Err())
		}
		return ErrCandidateSetup
	}
	for _, name := range referenced {
		if name == "geosite.dat" || name == "geoip.dat" {
			continue
		}
		if name != validator.userGeosite && name != validator.userGeoip && !companionAssetName.MatchString(name) {
			return ErrUnsupportedAsset
		}
	}

	candidateDir, err := validator.files.MkdirTemp(validator.tempRoot, "xray-candidate-*")
	if err != nil {
		setupErr := error(ErrCandidateSetup)
		if validCandidatePath(candidateDir, validator.tempRoot, validator.assetDir) {
			if cleanupErr := validator.files.RemoveAll(candidateDir); cleanupErr != nil {
				setupErr = errors.Join(setupErr, ErrCandidateCleanup)
			}
		}
		return setupErr
	}
	if !validCandidatePath(candidateDir, validator.tempRoot, validator.assetDir) {
		return ErrCandidateSetup
	}
	defer func() {
		if err := validator.files.RemoveAll(candidateDir); err != nil {
			resultErr = errors.Join(resultErr, ErrCandidateCleanup)
		}
	}()
	if err := validator.files.Chmod(candidateDir, candidateDirectoryMode); err != nil {
		return ErrCandidateSetup
	}

	for _, asset := range []struct {
		name string
		path string
	}{
		{name: "geosite.dat", path: downloaded.GeositePath},
		{name: "geoip.dat", path: downloaded.GeoipPath},
	} {
		if err := validator.copyAsset(ctx, asset.path, filepath.Join(candidateDir, asset.name)); err != nil {
			var size *geodatalimits.Exceeded
			if errors.As(err, &size) {
				return errors.Join(ErrCandidateSetup, size)
			}
			if ctx.Err() != nil {
				return errors.Join(ErrValidationCanceled, ctx.Err())
			}
			return ErrCandidateSetup
		}
	}

	sort.Strings(referenced)
	for _, name := range referenced {
		if name == "geosite.dat" || name == "geoip.dat" {
			continue
		}
		source := filepath.Join(validator.assetDir, name)
		if err := validator.copyAsset(ctx, source, filepath.Join(candidateDir, name)); err != nil {
			var size *geodatalimits.Exceeded
			if errors.As(err, &size) {
				return errors.Join(ErrCandidateSetup, size)
			}
			if ctx.Err() != nil {
				return errors.Join(ErrValidationCanceled, ctx.Err())
			}
			if errors.Is(err, os.ErrNotExist) {
				return ErrMissingCompanionAsset
			}
			return ErrCandidateSetup
		}
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrValidationCanceled, err)
	}

	args := []string{"run", "-test", "-config", validator.config}
	environment := []string{"XRAY_LOCATION_ASSET=" + candidateDir}
	if err := validator.runner.Run(ctx, validator.binary, args, environment); err != nil {
		if ctx.Err() != nil {
			return errors.Join(ErrValidationCanceled, ctx.Err())
		}
		return ErrXrayValidation
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrValidationCanceled, err)
	}
	return nil
}

// ValidateCustomGeosite validates one fixed referenced custom geosite file
// together with the other referenced live assets in an isolated Xray run.
func (validator *Validator) ValidateCustomGeosite(ctx context.Context, sourcePath, expectedHash string) error {
	return validator.validateCustomAssets(ctx, []stagedCustomAsset{
		{name: validator.userGeosite, source: sourcePath, expectedHash: expectedHash},
	})
}

// ValidateCustomAssets validates the configured custom geosite and geoip
// sources together before either live asset is replaced. The geosite must
// already be referenced by the current configuration, while the geoip may
// remain unreferenced until the new template is enabled.
func (validator *Validator) ValidateCustomAssets(ctx context.Context, geositePath, geositeHash, geoipPath, geoipHash string) error {
	return validator.validateCustomAssets(ctx, []stagedCustomAsset{
		{name: validator.userGeosite, source: geositePath, expectedHash: geositeHash},
		{name: validator.userGeoip, source: geoipPath, expectedHash: geoipHash},
	})
}

type stagedCustomAsset struct {
	name         string
	source       string
	expectedHash string
}

func (validator *Validator) validateCustomAssets(ctx context.Context, staged []stagedCustomAsset) (resultErr error) {
	if ctx == nil || len(staged) == 0 {
		return ErrInvalidInput
	}
	stagedNames := make(map[string]bool, len(staged))
	for _, asset := range staged {
		if asset.name != validator.userGeosite && asset.name != validator.userGeoip {
			return ErrInvalidInput
		}
		if stagedNames[asset.name] {
			return ErrInvalidInput
		}
		expectedSource := filepath.Join(validator.assetDir, customAssetPendingDir, asset.name)
		if !filepath.IsAbs(asset.source) || filepath.Clean(asset.source) != asset.source ||
			asset.source != expectedSource || !validSHA256(asset.expectedHash) {
			return ErrInvalidInput
		}
		stagedNames[asset.name] = true
	}
	if !stagedNames[validator.userGeosite] {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrValidationCanceled, err)
	}
	referenced, err := validator.assetReader.ReferencedExternalAssets(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return errors.Join(ErrValidationCanceled, ctx.Err())
		}
		return ErrCandidateSetup
	}
	found := false
	for _, name := range referenced {
		if name == validator.userGeosite {
			found = true
			continue
		}
		if name == "geosite.dat" || name == "geoip.dat" {
			continue
		}
		if name != validator.userGeosite && name != validator.userGeoip && !companionAssetName.MatchString(name) {
			return ErrUnsupportedAsset
		}
	}
	if !found {
		return ErrUnsupportedAsset
	}
	candidateDir, err := validator.files.MkdirTemp(validator.tempRoot, "xray-candidate-*")
	if err != nil {
		setupErr := error(ErrCandidateSetup)
		if validCandidatePath(candidateDir, validator.tempRoot, validator.assetDir) {
			if cleanupErr := validator.files.RemoveAll(candidateDir); cleanupErr != nil {
				setupErr = errors.Join(setupErr, ErrCandidateCleanup)
			}
		}
		return setupErr
	}
	if !validCandidatePath(candidateDir, validator.tempRoot, validator.assetDir) {
		if filepath.IsAbs(candidateDir) && filepath.Dir(candidateDir) == validator.tempRoot {
			_ = validator.files.RemoveAll(candidateDir)
		}
		return ErrCandidateSetup
	}
	defer func() {
		if err := validator.files.RemoveAll(candidateDir); err != nil {
			resultErr = errors.Join(resultErr, ErrCandidateCleanup)
		}
	}()
	if err := validator.files.Chmod(candidateDir, candidateDirectoryMode); err != nil {
		return ErrCandidateSetup
	}
	copiedHashes := make([]string, len(staged))
	for index, asset := range staged {
		hash, err := validator.copyAssetWithHash(ctx, asset.source, filepath.Join(candidateDir, asset.name))
		if err != nil {
			var size *geodatalimits.Exceeded
			if errors.As(err, &size) {
				return errors.Join(ErrCandidateSetup, size)
			}
			if ctx.Err() != nil {
				return errors.Join(ErrValidationCanceled, ctx.Err())
			}
			return ErrCandidateSetup
		}
		copiedHashes[index] = hash
	}
	for index, asset := range staged {
		if copiedHashes[index] != asset.expectedHash {
			return ErrCandidateChanged
		}
	}
	sort.Strings(referenced)
	for _, name := range referenced {
		if stagedNames[name] {
			continue
		}
		source := filepath.Join(validator.assetDir, name)
		if err := validator.copyAsset(ctx, source, filepath.Join(candidateDir, name)); err != nil {
			var size *geodatalimits.Exceeded
			if errors.As(err, &size) {
				return errors.Join(ErrCandidateSetup, size)
			}
			if ctx.Err() != nil {
				return errors.Join(ErrValidationCanceled, ctx.Err())
			}
			if errors.Is(err, os.ErrNotExist) {
				return ErrMissingCompanionAsset
			}
			return ErrCandidateSetup
		}
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrValidationCanceled, err)
	}
	args := []string{"run", "-test", "-config", validator.config}
	environment := []string{"XRAY_LOCATION_ASSET=" + candidateDir}
	if err := validator.runner.Run(ctx, validator.binary, args, environment); err != nil {
		if ctx.Err() != nil {
			return errors.Join(ErrValidationCanceled, ctx.Err())
		}
		return ErrXrayValidation
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrValidationCanceled, err)
	}
	return nil
}

func (validator *Validator) copyAsset(ctx context.Context, source, destination string) error {
	_, err := validator.copyAssetWithHash(ctx, source, destination)
	return err
}

func (validator *Validator) copyAssetWithHash(ctx context.Context, source, destination string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := validator.files.OpenSource(source)
	if err != nil {
		return "", err
	}
	info, statErr := file.Stat()
	if statErr != nil || info == nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return "", ErrCandidateSetup
	}
	if info.Size() > int64(validator.maxAssetSize) {
		_ = file.Close()
		return "", &geodatalimits.Exceeded{LimitBytes: validator.maxAssetSize}
	}
	output, err := validator.files.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, candidateAssetMode)
	if err != nil {
		_ = file.Close()
		return "", err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(contextReader{ctx: ctx, reader: file}, int64(validator.maxAssetSize)+1))
	if copyErr == nil && (n > int64(validator.maxAssetSize) || info.Size() != n) {
		copyErr = ErrCandidateSetup
	}
	closeOutputErr := output.Close()
	closeInputErr := file.Close()
	if copyErr != nil || closeOutputErr != nil || closeInputErr != nil {
		return "", ErrCandidateSetup
	}
	if err := validator.files.Chmod(destination, candidateAssetMode); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
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

func validDownloadResult(result *geodatadownload.Result, assetDir string) bool {
	if !filepath.IsAbs(result.Directory) || filepath.Clean(result.Directory) != result.Directory {
		return false
	}
	if isWithin(assetDir, result.Directory) || isWithin(result.Directory, assetDir) {
		return false
	}
	return result.GeositePath == filepath.Join(result.Directory, "geosite.dat") &&
		result.GeoipPath == filepath.Join(result.Directory, "geoip.dat")
}

func validCandidatePath(candidate, root, assetDir string) bool {
	return filepath.IsAbs(candidate) &&
		filepath.Clean(candidate) == candidate &&
		filepath.Dir(candidate) == root &&
		!isWithin(assetDir, candidate)
}

func isWithin(parent, path string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}
