// Package geodatadownload retrieves and verifies Xray geodata without
// installing assets or interacting with the Xray process.
package geodatadownload

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/geodatalimits"
)

const (
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
	ErrDNS                  = errors.New("geodata DNS lookup failed")
	ErrTLS                  = errors.New("geodata TLS connection failed")
	ErrConnection           = errors.New("geodata connection failed")
	ErrTimeout              = errors.New("geodata download timed out")
	ErrRedirect             = errors.New("geodata redirect rejected")
	ErrResponseTooLarge     = errors.New("geodata response exceeds size limit")
	ErrTruncatedResponse    = errors.New("geodata response was truncated")
	ErrInvalidChecksum      = errors.New("geodata checksum metadata is invalid")
	ErrChecksumMismatch     = errors.New("geodata checksum verification failed")
)

// WaitFunc waits between retries and can be replaced for deterministic tests.
type WaitFunc func(context.Context, time.Duration) error

// Options injects network and retry-wait dependencies. Non-positive
// timeouts select the package defaults.
type Options struct {
	MaxAssetSizeMB int
	Transport      http.RoundTripper
	Wait           WaitFunc
	RequestTimeout time.Duration
	OverallTimeout time.Duration
}

type Downloader struct {
	transport      http.RoundTripper
	wait           WaitFunc
	requestTimeout time.Duration
	overallTimeout time.Duration
	maxAssetSize   int64
}

// New creates a downloader with safe defaults for omitted dependencies.
func New(options Options) *Downloader {
	if options.Transport == nil {
		options.Transport = http.DefaultTransport
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
		wait:           options.Wait,
		requestTimeout: options.RequestTimeout,
		overallTimeout: options.OverallTimeout,
		maxAssetSize:   int64(geodatalimits.Bytes(options.MaxAssetSizeMB)),
	}
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
			return nil, retryableTransportError(requestContextErr, requestErr), transportError(requestContextErr, requestErr)
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
				return nil, true, &HTTPStatusError{Status: response.StatusCode}
			}
			return nil, false, &HTTPStatusError{Status: response.StatusCode}
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

func transportError(requestContextErr, cause error) error {
	if requestContextErr == context.DeadlineExceeded {
		return ErrTimeout
	}
	var dns *net.DNSError
	var tlsRecord tls.RecordHeaderError
	var certificate *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var network net.Error
	switch {
	case errors.As(cause, &dns):
		return errors.Join(ErrDownload, ErrDNS)
	case errors.As(cause, &tlsRecord), errors.As(cause, &certificate), errors.As(cause, &unknownAuthority):
		return errors.Join(ErrDownload, ErrTLS)
	case errors.As(cause, &network) && network.Timeout():
		return ErrTimeout
	default:
		return errors.Join(ErrDownload, ErrConnection)
	}
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
