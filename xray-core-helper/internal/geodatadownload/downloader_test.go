package geodatadownload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/geodatalimits"
)

func TestFetchAssetVerifiesEachFileIndependently(t *testing.T) {
	assets := validAssets()
	assets["geosite"] = []byte("tiny but checksum-valid")
	server := newAssetServer(t, assets)
	downloader := New(Options{Transport: server.Client().Transport, Wait: noWait})
	cfg := configFor(server.URL)
	for _, name := range []string{"geosite", "geoip"} {
		data, err := downloader.FetchAsset(context.Background(), cfg, name+".dat")
		if err != nil || !bytes.Equal(data, assets[name]) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	cfg.Geosite.URL = server.URL + "/missing"
	if _, err := downloader.FetchAsset(context.Background(), cfg, "geosite.dat"); !errors.Is(err, ErrDownload) {
		t.Fatal(err)
	}
	if _, err := downloader.FetchAsset(context.Background(), cfg, "geoip.dat"); err != nil {
		t.Fatal("failure of geosite affected geoip", err)
	}
}

func TestDownloadAcceptsGeositeAbovePrevious64MiBLimit(t *testing.T) {
	// Observed size of the configured geosite source on the acceptance server.
	const size = 73703302
	assets := validAssets()
	assets["geosite"] = bytes.Repeat([]byte("g"), size)
	server := newAssetServer(t, assets)
	downloader := New(Options{Transport: server.Client().Transport, Wait: noWait})
	data, err := downloader.FetchAsset(context.Background(), configFor(server.URL), "geosite.dat")
	if err != nil || len(data) != size {
		t.Fatalf("downloaded bytes=%d: %v", len(data), err)
	}

}

func TestDownloadRejectsDeclaredSizeAbove256MB(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "268435457")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	root := t.TempDir()
	downloader := New(Options{Transport: server.Client().Transport, Wait: noWait})
	_, err := downloader.FetchAsset(context.Background(), configFor(server.URL), "geosite.dat")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v; want size rejection", err)
	}
	assertEmpty(t, root)
}

func TestDownloadUsesConfiguredSizeLimitAndReportsIt(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048577")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	config := configFor(server.URL)
	config.MaxAssetSizeMB = 1
	downloader := New(Options{Transport: server.Client().Transport, Wait: noWait})
	_, err := downloader.FetchAsset(context.Background(), config, "geosite.dat")
	var exceeded *geodatalimits.Exceeded
	if !errors.As(err, &exceeded) || exceeded.LimitBytes != 1024*1024 {
		t.Fatalf("missing configured limit: %v", err)
	}
	if !strings.Contains(exceeded.Error(), "1 МБ") {
		t.Fatal(exceeded.Error())
	}
}

func TestDownloadRedirectPolicy(t *testing.T) {
	tests := []struct {
		name            string
		location        func(*httptest.Server) string
		wantErr         error
		wantFollowed    bool
		addSecondOrigin bool
	}{
		{
			name: "same-origin relative redirect",
			location: func(*httptest.Server) string {
				return "/geosite.dat"
			},
			wantFollowed: true,
		},
		{
			name: "cross-origin even when both origins are allowed",
			location: func(second *httptest.Server) string {
				return second.URL + "/geosite.dat"
			},
			wantErr:         ErrRedirect,
			addSecondOrigin: true,
		},
		{
			name: "HTTPS downgrade",
			location: func(*httptest.Server) string {
				return "http://data.example/geosite.dat"
			},
			wantErr: ErrRedirect,
		},
		{
			name: "untrusted initial URL",
			location: func(*httptest.Server) string {
				return ""
			},
			wantErr: ErrInvalidConfiguration,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assets := validAssets()
			var first *httptest.Server
			var second *httptest.Server
			secondHits := atomic.Int32{}
			second = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secondHits.Add(1)
				serveAsset(w, r, assets)
			}))
			t.Cleanup(second.Close)
			first = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirect-geosite" {
					http.Redirect(w, r, test.location(second), http.StatusFound)
					return
				}
				serveAsset(w, r, assets)
			}))
			t.Cleanup(first.Close)
			config := configFor(first.URL)
			config.Geosite.URL = first.URL + "/redirect-geosite"
			if test.name == "untrusted initial URL" {
				config.Geosite.URL = "https://untrusted.example/geosite.dat"
			}
			if test.addSecondOrigin {
				config.Origins = append(config.Origins, originFor(second.URL))
			}
			root := t.TempDir()
			downloader := New(Options{
				Transport: &hostRestrictedTransport{
					delegate: first.Client().Transport,
					host:     first.Listener.Addr().String(),
				},
				Wait: noWait,
			})
			_, err := downloader.FetchAsset(context.Background(), config, "geosite.dat")
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Download() error = %v, want %v", err, test.wantErr)
				}
				assertEmpty(t, root)
				if secondHits.Load() != 0 {
					t.Errorf("cross-origin target was contacted %d times", secondHits.Load())
				}
				return
			}
			if err != nil {
				t.Fatalf("Download() error = %v", err)
			}
			if !test.wantFollowed {
				t.Fatal("test case must specify its expected redirect result")
			}
		})
	}
}

func TestDownloadTimeoutAndCancellation(t *testing.T) {
	t.Run("per-request timeout", func(t *testing.T) {
		hits := atomic.Int32{}
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			<-r.Context().Done()
		}))
		defer server.Close()
		root := t.TempDir()
		downloader := New(Options{
			Transport:      server.Client().Transport,
			Wait:           noWait,
			RequestTimeout: 100 * time.Millisecond,
		})
		_, err := downloader.FetchAsset(context.Background(), configFor(server.URL), "geosite.dat")
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("Download() error = %v, want timeout", err)
		}
		if hits.Load() != maximumAttempts {
			t.Errorf("timed-out GET attempts = %d, want %d", hits.Load(), maximumAttempts)
		}
		assertEmpty(t, root)
	})

	t.Run("overall timeout", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer server.Close()
		root := t.TempDir()
		downloader := New(Options{
			Transport:      server.Client().Transport,
			Wait:           noWait,
			RequestTimeout: time.Second,
			OverallTimeout: 20 * time.Millisecond,
		})
		_, err := downloader.FetchAsset(context.Background(), configFor(server.URL), "geosite.dat")
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("Download() error = %v, want overall timeout", err)
		}
		assertEmpty(t, root)
	})

	t.Run("caller cancellation is not retried", func(t *testing.T) {
		entered := make(chan struct{}, 1)
		hits := atomic.Int32{}
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			entered <- struct{}{}
			<-r.Context().Done()
		}))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		root := t.TempDir()
		downloader := New(Options{
			Transport: server.Client().Transport,
			Wait:      noWait,
		})
		done := make(chan error, 1)
		go func() {
			_, err := downloader.FetchAsset(ctx, configFor(server.URL), "geosite.dat")
			done <- err
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("request did not reach httptest server")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Download() error = %v, want context.Canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Download() did not return after cancellation")
		}
		if hits.Load() != 1 {
			t.Errorf("requests after cancellation = %d, want one", hits.Load())
		}
		assertEmpty(t, root)
	})
}

func TestDownloadRetriesOnlyTransientFailures(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantErr  bool
		wantHits int32
	}{
		{name: "retry service unavailable", status: http.StatusServiceUnavailable, wantHits: 2},
		{name: "do not retry not found", status: http.StatusNotFound, wantErr: true, wantHits: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assets := validAssets()
			hits := atomic.Int32{}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/geosite.dat" && hits.Add(1) == 1 {
					http.Error(w, http.StatusText(test.status), test.status)
					return
				}
				serveAsset(w, r, assets)
			}))
			defer server.Close()
			waitCalls := 0
			downloader := New(Options{
				Transport: server.Client().Transport,
				Wait: func(context.Context, time.Duration) error {
					waitCalls++
					return nil
				},
			})
			_, err := downloader.FetchAsset(context.Background(), configFor(server.URL), "geosite.dat")
			if test.wantErr {
				if !errors.Is(err, ErrDownload) {
					t.Fatalf("Download() error = %v, want download error", err)
				}
			} else {
				if err != nil {
					t.Fatalf("Download() error = %v", err)
				}
			}
			if got := hits.Load(); got != test.wantHits {
				t.Errorf("geosite attempts = %d, want %d", got, test.wantHits)
			}
			if wantWaits := int(test.wantHits - 1); waitCalls != wantWaits {
				t.Errorf("retry waits = %d, want %d", waitCalls, wantWaits)
			}
		})
	}
}

func TestDownloadRejectsMalformedAndIncorrectChecksums(t *testing.T) {
	tests := []struct {
		name     string
		checksum func([]byte) []byte
		wantErr  error
	}{
		{
			name: "malformed metadata",
			checksum: func([]byte) []byte {
				return []byte("not a checksum\n")
			},
			wantErr: ErrInvalidChecksum,
		},
		{
			name: "wrong expected asset name",
			checksum: func(content []byte) []byte {
				digest := sha256.Sum256(content)
				return []byte(fmt.Sprintf("%x  geoip.dat\n", digest))
			},
			wantErr: ErrInvalidChecksum,
		},
		{
			name: "digest mismatch",
			checksum: func([]byte) []byte {
				return []byte(strings.Repeat("0", sha256.Size*2) + "  geosite.dat\n")
			},
			wantErr: ErrChecksumMismatch,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assets := validAssets()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/geosite.dat.sha256" {
					_, _ = w.Write(test.checksum(assets["geosite"]))
					return
				}
				serveAsset(w, r, assets)
			}))
			defer server.Close()
			root := t.TempDir()
			_, err := New(Options{
				Transport: server.Client().Transport,
				Wait:      noWait,
			}).FetchAsset(context.Background(), configFor(server.URL), "geosite.dat")
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Download() error = %v, want %v", err, test.wantErr)
			}
			assertEmpty(t, root)
		})
	}
}

func TestDownloadEnforcesAssetSizeAndTruncationLimits(t *testing.T) {
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request, map[string][]byte)
		wantErr error
		maxSize int64
	}{
		{
			name: "oversized declared content length",
			handler: func(w http.ResponseWriter, r *http.Request, assets map[string][]byte) {
				if r.URL.Path == "/geosite.dat" {
					w.Header().Set("Content-Length", "65")
					_, _ = w.Write(bytes.Repeat([]byte("x"), 65))
					return
				}
				serveAsset(w, r, assets)
			},
			wantErr: ErrResponseTooLarge,
			maxSize: 64,
		},
		{
			name: "oversized chunked response",
			handler: func(w http.ResponseWriter, r *http.Request, assets map[string][]byte) {
				if r.URL.Path == "/geosite.dat" {
					flusher := w.(http.Flusher)
					flusher.Flush()
					_, _ = w.Write(bytes.Repeat([]byte("x"), 65))
					return
				}
				serveAsset(w, r, assets)
			},
			wantErr: ErrResponseTooLarge,
			maxSize: 64,
		},
		{
			name: "truncated response",
			handler: func(w http.ResponseWriter, r *http.Request, assets map[string][]byte) {
				if r.URL.Path == "/geosite.dat" {
					w.Header().Set("Content-Length", "2048")
					_, _ = w.Write(bytes.Repeat([]byte("t"), 1024))
					return
				}
				serveAsset(w, r, assets)
			},
			wantErr: ErrTruncatedResponse,
		},
		{
			name: "oversized checksum metadata",
			handler: func(w http.ResponseWriter, r *http.Request, assets map[string][]byte) {
				if r.URL.Path == "/geosite.dat.sha256" {
					_, _ = w.Write(bytes.Repeat([]byte("x"), maximumChecksumSize+1))
					return
				}
				serveAsset(w, r, assets)
			},
			wantErr: ErrResponseTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assets := validAssets()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				test.handler(w, r, assets)
			}))
			defer server.Close()
			root := t.TempDir()
			downloader := New(Options{
				Transport: server.Client().Transport,
				Wait:      noWait,
			})
			if test.maxSize != 0 {
				downloader.maxAssetSize = test.maxSize
			}
			_, err := downloader.FetchAsset(context.Background(), configFor(server.URL), "geosite.dat")
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Download() error = %v, want %v", err, test.wantErr)
			}
			assertEmpty(t, root)
		})
	}
}

func TestDownloadDoesNotUseLiveInternetForUntrustedSources(t *testing.T) {
	server := newAssetServer(t, validAssets())
	transport := &hostRestrictedTransport{
		delegate: server.Client().Transport,
		host:     server.Listener.Addr().String(),
	}
	config := configFor(server.URL)
	config.Geosite.URL = "https://not-configured.invalid/geosite.dat"
	_, err := New(Options{
		Transport: transport,
		Wait:      noWait,
	}).FetchAsset(context.Background(), config, "geosite.dat")
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("Download() error = %v, want untrusted URL rejection", err)
	}
	if transport.calls != 0 {
		t.Errorf("untrusted source caused %d HTTP requests", transport.calls)
	}
}

func configFor(baseURL string) bootstrap.GeodataConfig {
	return bootstrap.GeodataConfig{
		Origins: []string{originFor(baseURL)},
		Geosite: bootstrap.GeodataSourceConfig{
			URL: baseURL + "/geosite.dat",
			Checksum: bootstrap.GeodataChecksumConfig{
				URL:       baseURL + "/geosite.dat.sha256",
				Algorithm: "sha256",
			},
		},
		Geoip: bootstrap.GeodataSourceConfig{
			URL: baseURL + "/geoip.dat",
			Checksum: bootstrap.GeodataChecksumConfig{
				URL:       baseURL + "/geoip.dat.sha256",
				Algorithm: "sha256",
			},
		},
	}
}

func originFor(rawURL string) string {
	parsed, _ := url.Parse(rawURL)
	return parsed.Scheme + "://" + parsed.Host
}

func validAssets() map[string][]byte {
	return map[string][]byte{
		"geosite": bytes.Repeat([]byte("g"), 1024+17),
		"geoip":   bytes.Repeat([]byte("i"), 1024+23),
	}
}

func newAssetServer(t *testing.T, assets map[string][]byte) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, r, assets)
	}))
	t.Cleanup(server.Close)
	return server
}

func serveAsset(w http.ResponseWriter, r *http.Request, assets map[string][]byte) {
	switch r.URL.Path {
	case "/geosite.dat":
		_, _ = w.Write(assets["geosite"])
	case "/geoip.dat":
		_, _ = w.Write(assets["geoip"])
	case "/geosite.dat.sha256":
		writeChecksum(w, "geosite.dat", assets["geosite"])
	case "/geoip.dat.sha256":
		writeChecksum(w, "geoip.dat", assets["geoip"])
	default:
		http.NotFound(w, r)
	}
}

func writeChecksum(w http.ResponseWriter, name string, content []byte) {
	digest := sha256.Sum256(content)
	_, _ = fmt.Fprintf(w, "%x  %s\n", digest, name)
}

type hostRestrictedTransport struct {
	delegate http.RoundTripper
	host     string
	calls    int
}

func (transport *hostRestrictedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != transport.host {
		return nil, fmt.Errorf("test transport rejects non-httptest destination")
	}
	transport.calls++
	return transport.delegate.RoundTrip(request)
}

func noWait(context.Context, time.Duration) error {
	return nil
}

func assertEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary root still contains partial output: %v", entries)
	}
}

func TestTransportErrorsHaveSafeClassifications(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cause, want error
	}{
		{"dns", &net.DNSError{Err: "private resolver details", Name: "secret.example"}, ErrDNS},
		{"tls", &tls.CertificateVerificationError{Err: errors.New("private certificate")}, ErrTLS},
		{"connection", errors.New("https://user:password@secret.example"), ErrConnection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := transportError(nil, tc.cause)
			if !errors.Is(err, tc.want) || !errors.Is(err, ErrDownload) {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
				t.Fatal("unsafe error", err)
			}
		})
	}
}
