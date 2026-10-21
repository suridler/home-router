// Package candidatevalidation validates downloaded geodata against Xray
// without changing the configured asset directory.
package candidatevalidation

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/geodatalimits"
	"home-router/xray-core-helper/internal/xrayconfig"
)

const (
	candidateDirectoryMode = 0700
	candidateAssetMode     = 0600
)

var (
	ErrInvalidInput     = errors.New("invalid candidate validation input")
	ErrCandidateSetup   = errors.New("candidate asset setup failed")
	ErrUnsupportedAsset = errors.New("unsupported external asset reference")
	ErrXrayValidation   = errors.New("Xray rejected candidate assets")
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

func (validator *Validator) copyAsset(ctx context.Context, source, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := validator.files.OpenSource(source)
	if err != nil {
		return err
	}
	info, statErr := file.Stat()
	if statErr != nil || info == nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return ErrCandidateSetup
	}
	if info.Size() > int64(validator.maxAssetSize) {
		_ = file.Close()
		return &geodatalimits.Exceeded{LimitBytes: validator.maxAssetSize}
	}
	output, err := validator.files.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, candidateAssetMode)
	if err != nil {
		_ = file.Close()
		return err
	}
	n, copyErr := io.Copy(output, io.LimitReader(contextReader{ctx: ctx, reader: file}, int64(validator.maxAssetSize)+1))
	if copyErr == nil && (n > int64(validator.maxAssetSize) || info.Size() != n) {
		copyErr = ErrCandidateSetup
	}
	closeOutputErr := output.Close()
	closeInputErr := file.Close()
	if copyErr != nil || closeOutputErr != nil || closeInputErr != nil {
		return ErrCandidateSetup
	}
	if err := validator.files.Chmod(destination, candidateAssetMode); err != nil {
		return err
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

func isWithin(parent, path string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}
