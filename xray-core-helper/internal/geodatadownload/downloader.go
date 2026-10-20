// Package geodatadownload retrieves and verifies Xray geodata without
// installing assets or interacting with the Xray process.
package geodatadownload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/geodatalimits"
)

const (
	assetMode             = 0600
	directoryMode         = 0700
	minimumAssetSize      = 1024
	maximumChecksumSize   = 4096
	maximumRedirects      = 5
	maximumAttempts       = 3
	defaultRequestTimeout = 15 * time.Second
	defaultOverallTimeout = 2 * time.Minute
	retryDelay            = 100 * time.Millisecond
)

var (
	ErrInvalidConfiguration = errors.New("invalid geodata configuration")
	ErrSourcesDisabled      = errors.New("geodata sources are disabled")
	ErrDownload             = errors.New("geodata download failed")
	ErrTimeout              = errors.New("geodata download timed out")
	ErrRedirect             = errors.New("geodata redirect rejected")
	ErrResponseTooLarge     = errors.New("geodata response exceeds size limit")
	ErrAssetTooSmall        = errors.New("geodata asset is smaller than the minimum size")
	ErrTruncatedResponse    = errors.New("geodata response was truncated")
	ErrInvalidChecksum      = errors.New("geodata checksum metadata is invalid")
	ErrChecksumMismatch     = errors.New("geodata checksum verification failed")
)

// File is the writable, closable file handle used for private download output.
type File interface {
	io.Writer
	Close() error
}

// FileSystem is the limited filesystem surface used to own the temporary result.
type FileSystem interface {
	MkdirTemp(dir, pattern string) (string, error)
	Chmod(name string, mode os.FileMode) error
	OpenFile(name string, flag int, perm os.FileMode) (File, error)
	RemoveAll(path string) error
}

// WaitFunc waits between retries and can be replaced for deterministic tests.
type WaitFunc func(context.Context, time.Duration) error

// Options injects network, filesystem and retry-wait dependencies. Non-positive
// timeouts select the package defaults.
type Options struct {
	MaxAssetSizeMB int
	Transport      http.RoundTripper
	FileSystem     FileSystem
	TempRoot       string
	Wait           WaitFunc
	RequestTimeout time.Duration
	OverallTimeout time.Duration
}

type Downloader struct {
	transport      http.RoundTripper
	files          FileSystem
	tempRoot       string
	wait           WaitFunc
	requestTimeout time.Duration
	overallTimeout time.Duration
	maxAssetSize   int64
}

// Result owns its private directory until Cleanup is called. Its paths remain
// valid only until cleanup; callers must not move or remove the directory.
// Cleanup is safe to call repeatedly.
type Result struct {
	Directory           string
	GeositePath         string
	GeoipPath           string
	GeositeChecksumPath string
	GeoipChecksumPath   string
	cleanupState        *cleanupState
}

type cleanupState struct {
	once sync.Once
	err  error
	fs   FileSystem
	dir  string
}

type osFileSystem struct{}

func (osFileSystem) MkdirTemp(dir, pattern string) (string, error) {
	return os.MkdirTemp(dir, pattern)
}

func (osFileSystem) Chmod(name string, mode os.FileMode) error {
	return os.Chmod(name, mode)
}

func (osFileSystem) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	return os.OpenFile(name, flag, perm)
}

func (osFileSystem) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

// New creates a downloader with safe defaults for omitted dependencies.
func New(options Options) *Downloader {
	if options.Transport == nil {
		options.Transport = http.DefaultTransport
	}
	if options.FileSystem == nil {
		options.FileSystem = osFileSystem{}
	}
	if options.Wait == nil {
		options.Wait = waitContext
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = defaultRequestTimeout
	}
	if options.OverallTimeout <= 0 {
		options.OverallTimeout = defaultOverallTimeout
	}
	return &Downloader{
		transport:      options.Transport,
		files:          options.FileSystem,
		tempRoot:       options.TempRoot,
		wait:           options.Wait,
		requestTimeout: options.RequestTimeout,
		overallTimeout: options.OverallTimeout,
		maxAssetSize:   int64(geodatalimits.Bytes(options.MaxAssetSizeMB)),
	}
}

// Download retrieves and verifies both assets without installing or otherwise
// modifying anything outside a newly created private temporary directory.
func (downloader *Downloader) Download(ctx context.Context, config bootstrap.GeodataConfig) (_ *Result, err error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfiguration, err)
	}
	if config.Geosite.URL == "" || config.Geoip.URL == "" ||
		config.Geosite.Checksum.URL == "" || config.Geoip.Checksum.URL == "" {
		return nil, ErrSourcesDisabled
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrInvalidConfiguration)
	}

	overallContext, cancel := context.WithTimeout(ctx, downloader.overallTimeout)
	defer cancel()

	directory, err := downloader.files.MkdirTemp(downloader.tempRoot, "xray-geodata-*")
	if err != nil {
		return nil, fmt.Errorf("create geodata temporary directory: %w", err)
	}
	removePartial := func() error {
		if removeErr := downloader.files.RemoveAll(directory); removeErr != nil {
			return fmt.Errorf("remove partial geodata directory: %w", removeErr)
		}
		return nil
	}
	defer func() {
		if err != nil {
			if cleanupErr := removePartial(); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
		}
	}()
	if err := downloader.files.Chmod(directory, directoryMode); err != nil {
		return nil, fmt.Errorf("secure geodata temporary directory: %w", err)
	}

	assets := []struct {
		name   string
		source bootstrap.GeodataSourceConfig
	}{
		{name: "geosite.dat", source: config.Geosite},
		{name: "geoip.dat", source: config.Geoip},
	}
	paths := make(map[string]string, len(assets)*2)
	for _, asset := range assets {
		limit := min(downloader.maxAssetSize, int64(geodatalimits.Bytes(config.MaxAssetSizeMB)))
		content, fetchErr := downloader.fetch(overallContext, config, asset.source.URL, limit)
		if fetchErr != nil {
			if errors.Is(fetchErr, ErrResponseTooLarge) {
				fetchErr = errors.Join(fetchErr, &geodatalimits.Exceeded{LimitBytes: int(limit)})
			}
			return nil, fmt.Errorf("%s: %w", asset.name, fetchErr)
		}
		if len(content) <= minimumAssetSize {
			return nil, fmt.Errorf("%s: %w", asset.name, ErrAssetTooSmall)
		}
		checksum, fetchErr := downloader.fetch(overallContext, config, asset.source.Checksum.URL, maximumChecksumSize)
		if fetchErr != nil {
			return nil, fmt.Errorf("%s checksum: %w", asset.name, fetchErr)
		}
		expected, parseErr := parseChecksum(checksum, asset.name)
		if parseErr != nil {
			return nil, fmt.Errorf("%s checksum: %w", asset.name, parseErr)
		}
		actual := sha256.Sum256(content)
		if subtle.ConstantTimeCompare(expected, actual[:]) != 1 {
			return nil, fmt.Errorf("%s: %w", asset.name, ErrChecksumMismatch)
		}
		path := filepath.Join(directory, asset.name)
		if writeErr := downloader.writePrivateFile(path, content); writeErr != nil {
			return nil, fmt.Errorf("write %s: %w", asset.name, writeErr)
		}
		checksumPath := filepath.Join(directory, asset.name+".sha256")
		if writeErr := downloader.writePrivateFile(checksumPath, checksum); writeErr != nil {
			return nil, fmt.Errorf("write %s checksum: %w", asset.name, writeErr)
		}
		paths[asset.name] = path
		paths[asset.name+".sha256"] = checksumPath
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if overallContext.Err() != nil {
		return nil, ErrTimeout
	}
	return &Result{
		Directory:           directory,
		GeositePath:         paths["geosite.dat"],
		GeoipPath:           paths["geoip.dat"],
		GeositeChecksumPath: paths["geosite.dat.sha256"],
		GeoipChecksumPath:   paths["geoip.dat.sha256"],
		cleanupState: &cleanupState{
			fs:  downloader.files,
			dir: directory,
		},
	}, nil
}

// Cleanup releases the result's directory. Repeated calls return the same
// cleanup error, if any, and never change ownership to another component.
func (result *Result) Cleanup() error {
	if result == nil || result.cleanupState == nil {
		return nil
	}
	state := result.cleanupState
	state.once.Do(func() {
		state.err = state.fs.RemoveAll(state.dir)
	})
	if state.err != nil {
		return fmt.Errorf("remove geodata temporary directory: %w", state.err)
	}
	return nil
}

func (downloader *Downloader) writePrivateFile(path string, content []byte) (err error) {
	file, err := downloader.files.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, assetMode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	if err := downloader.files.Chmod(path, assetMode); err != nil {
		return err
	}
	_, err = io.Copy(file, bytes.NewReader(content))
	return err
}

func (downloader *Downloader) fetch(ctx context.Context, config bootstrap.GeodataConfig, rawURL string, maximum int64) ([]byte, error) {
	for attempt := 0; attempt < maximumAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, contextError(ctx, err)
		}
		content, retryable, err := downloader.fetchAttempt(ctx, config, rawURL, maximum)
		if err == nil {
			return content, nil
		}
		if !retryable || attempt == maximumAttempts-1 {
			return nil, err
		}
		if err := downloader.wait(ctx, retryDelay*time.Duration(1<<attempt)); err != nil {
			return nil, contextError(ctx, err)
		}
	}
	return nil, ErrDownload
}

func (downloader *Downloader) fetchAttempt(ctx context.Context, config bootstrap.GeodataConfig, rawURL string, maximum int64) ([]byte, bool, error) {
	if !config.AllowsRemoteURL(rawURL) {
		return nil, false, ErrInvalidConfiguration
	}
	original, err := url.Parse(rawURL)
	if err != nil {
		return nil, false, ErrInvalidConfiguration
	}
	current := original
	for redirects := 0; ; redirects++ {
		requestContext, cancel := context.WithTimeout(ctx, downloader.requestTimeout)
		request, requestErr := http.NewRequestWithContext(requestContext, http.MethodGet, current.String(), nil)
		if requestErr != nil {
			cancel()
			return nil, false, ErrInvalidConfiguration
		}
		request.Header.Set("Accept-Encoding", "identity")
		response, requestErr := downloader.transport.RoundTrip(request)
		if requestErr != nil {
			requestContextErr := requestContext.Err()
			cancel()
			if ctx.Err() != nil {
				return nil, false, contextError(ctx, ctx.Err())
			}
			return nil, retryableTransportError(requestContextErr, requestErr), transportError(requestContextErr)
		}
		if response == nil || response.Body == nil {
			cancel()
			return nil, false, ErrDownload
		}
		if isRedirect(response.StatusCode) {
			location := response.Header.Get("Location")
			_ = response.Body.Close()
			cancel()
			if redirects >= maximumRedirects || location == "" || strings.Contains(location, "#") {
				return nil, false, ErrRedirect
			}
			reference, parseErr := url.Parse(location)
			if parseErr != nil {
				return nil, false, ErrRedirect
			}
			target := current.ResolveReference(reference)
			if !config.AllowsRemoteURL(target.String()) || !sameOrigin(original, target) {
				return nil, false, ErrRedirect
			}
			current = target
			continue
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			cancel()
			if transientStatus(response.StatusCode) {
				return nil, true, ErrDownload
			}
			return nil, false, ErrDownload
		}
		encoding := strings.TrimSpace(response.Header.Get("Content-Encoding"))
		if encoding != "" && !strings.EqualFold(encoding, "identity") {
			_ = response.Body.Close()
			cancel()
			return nil, false, ErrDownload
		}
		if response.ContentLength > maximum {
			_ = response.Body.Close()
			cancel()
			return nil, false, ErrResponseTooLarge
		}
		limited := io.LimitReader(response.Body, maximum+1)
		content, readErr := io.ReadAll(limited)
		closeErr := response.Body.Close()
		requestErr = requestContext.Err()
		cancel()
		if ctx.Err() != nil {
			return nil, false, contextError(ctx, ctx.Err())
		}
		if requestErr == context.DeadlineExceeded {
			return nil, true, ErrTimeout
		}
		if readErr != nil {
			if errors.Is(readErr, io.ErrUnexpectedEOF) || isTemporaryError(readErr) {
				return nil, true, ErrTruncatedResponse
			}
			return nil, false, ErrDownload
		}
		if closeErr != nil {
			return nil, false, ErrDownload
		}
		if int64(len(content)) > maximum {
			return nil, false, ErrResponseTooLarge
		}
		if response.ContentLength >= 0 && int64(len(content)) != response.ContentLength {
			return nil, true, ErrTruncatedResponse
		}
		return content, false, nil
	}
}

func parseChecksum(content []byte, expectedName string) ([]byte, error) {
	line := string(content)
	if strings.HasSuffix(line, "\n") {
		line = strings.TrimSuffix(line, "\n")
	}
	line = strings.TrimSuffix(line, "\r")
	if strings.ContainsAny(line, "\r\n") || len(line) < sha256.Size*2+2 ||
		line[sha256.Size*2] != ' ' {
		return nil, ErrInvalidChecksum
	}
	digestText := line[:sha256.Size*2]
	name := line[sha256.Size*2+1:]
	if name == " "+expectedName {
		name = expectedName
	} else if strings.HasPrefix(name, "*") {
		name = name[1:]
	}
	if name != expectedName {
		return nil, ErrInvalidChecksum
	}
	digest, err := hex.DecodeString(digestText)
	if err != nil || len(digest) != sha256.Size {
		return nil, ErrInvalidChecksum
	}
	return digest, nil
}

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func transientStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryableTransportError(requestContextErr error, err error) bool {
	if requestContextErr == context.DeadlineExceeded {
		return true
	}
	return isTemporaryError(err)
}

func isTemporaryError(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

func transportError(requestContextErr error) error {
	if requestContextErr == context.DeadlineExceeded {
		return ErrTimeout
	}
	return ErrDownload
}

func contextError(ctx context.Context, err error) error {
	if ctx.Err() == context.Canceled {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	return ErrDownload
}

func sameOrigin(first, second *url.URL) bool {
	return origin(first) == origin(second)
}

func origin(parsed *url.URL) string {
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := parsed.Port()
	if port == "" {
		port = strconv.Itoa(443)
	}
	return strings.ToLower(parsed.Scheme) + "://" + host + ":" + port
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
