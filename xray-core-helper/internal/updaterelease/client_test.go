package updaterelease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func redirectResponse(location string) *http.Response {
	response := response(http.StatusFound, "")
	response.Header.Set("Location", location)
	return response
}

func testAssets(baseURL, version, arch string) []apiAsset {
	archive, cli, checksums := assetNames(version, arch)
	names := []string{archive, cli, checksums}
	assets := make([]apiAsset, 0, len(names))
	for _, name := range names {
		assets = append(assets, apiAsset{
			Name:               name,
			BrowserDownloadURL: baseURL + "/download/" + name,
		})
	}
	return assets
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func TestReleaseJSONFieldNames(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(Release{
		Version:      "helper-v1.2.3",
		ArchiveName:  "archive",
		ArchiveURL:   "archive-url",
		ChecksumsURL: "checksums-url",
		CLIName:      "cli",
		CLIURL:       "cli-url",
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"version": true, "archiveName": true, "archiveUrl": true,
		"checksumsUrl": true, "cliName": true, "cliUrl": true,
	}
	if len(decoded) != len(want) {
		t.Fatalf("Release JSON keys = %v", decoded)
	}
	for key := range want {
		if _, exists := decoded[key]; !exists {
			t.Errorf("missing Release JSON key %q", key)
		}
	}
}

func TestNewClientDefaults(t *testing.T) {
	t.Parallel()
	client := NewClient()
	if client.APIBase != "https://api.github.com/repos/suridler/home-router" {
		t.Fatalf("APIBase = %q", client.APIBase)
	}
	if client.HTTP == nil || client.HTTP.Timeout != 30*time.Second {
		t.Fatalf("HTTP client = %#v", client.HTTP)
	}
	if !client.productionMode() {
		t.Fatal("NewClient must use the production GitHub policy")
	}
}

func TestResolveSelectsExactPublishedTag(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path != "/releases/tags/helper-v1.2.3" {
			t.Errorf("path = %q", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if request.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent")
		}
		writeJSON(t, writer, apiRelease{
			TagName: "helper-v1.2.3",
			Assets:  testAssets(server.URL, "helper-v1.2.3", "amd64"),
		})
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client(), APIBase: server.URL}
	release, err := client.Resolve(context.Background(), "helper-v1.2.3", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	archive, cli, checksums := assetNames("helper-v1.2.3", "amd64")
	if release.Version != "helper-v1.2.3" || release.ArchiveName != archive ||
		release.CLIName != cli || release.ChecksumsURL != server.URL+"/download/"+checksums ||
		release.ArchiveURL != server.URL+"/download/"+archive ||
		release.CLIURL != server.URL+"/download/"+cli {
		t.Fatalf("unexpected release: %#v", release)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestResolveRejectsDraftAndMissingTag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		release apiRelease
	}{
		{
			name: "draft",
			release: apiRelease{
				TagName: "helper-v1.2.3",
				Draft:   true,
				Assets:  testAssets("http://example.test", "helper-v1.2.3", "amd64"),
			},
		},
		{
			name: "tag mismatch",
			release: apiRelease{
				TagName: "helper-v1.2.4",
				Assets:  testAssets("http://example.test", "helper-v1.2.4", "amd64"),
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writeJSON(t, writer, test.release)
			}))
			defer server.Close()
			client := &Client{HTTP: server.Client(), APIBase: server.URL}
			_, err := client.Resolve(context.Background(), "helper-v1.2.3", "amd64")
			if !errors.Is(err, ErrReleaseNotFound) {
				t.Fatalf("error = %v, want ErrReleaseNotFound", err)
			}
		})
	}
}

func TestResolveRejectsMissingAndDuplicateAssets(t *testing.T) {
	t.Parallel()
	archive, _, checksums := assetNames("helper-v2.3.4-beta.1", "arm64")
	for _, missing := range []string{archive, checksums} {
		missing := missing
		t.Run("missing/"+missing, func(t *testing.T) {
			t.Parallel()
			assets := testAssets("http://example.test", "helper-v2.3.4-beta.1", "arm64")
			filtered := assets[:0]
			for _, asset := range assets {
				if asset.Name != missing {
					filtered = append(filtered, asset)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writeJSON(t, writer, apiRelease{TagName: "helper-v2.3.4-beta.1", Assets: filtered})
			}))
			defer server.Close()
			client := &Client{HTTP: server.Client(), APIBase: server.URL}
			_, err := client.Resolve(context.Background(), "helper-v2.3.4-beta.1", "arm64")
			if !errors.Is(err, ErrInvalidRelease) {
				t.Fatalf("error = %v, want ErrInvalidRelease", err)
			}
		})
	}
	t.Run("duplicate", func(t *testing.T) {
		t.Parallel()
		assets := testAssets("http://example.test", "helper-v2.3.4-beta.1", "arm64")
		assets = append(assets, assets[0])
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, apiRelease{TagName: "helper-v2.3.4-beta.1", Assets: assets})
		}))
		defer server.Close()
		client := &Client{HTTP: server.Client(), APIBase: server.URL}
		_, err := client.Resolve(context.Background(), "helper-v2.3.4-beta.1", "arm64")
		if !errors.Is(err, ErrInvalidRelease) {
			t.Fatalf("error = %v, want ErrInvalidRelease", err)
		}
	})
}

func TestResolveDoesNotContactAPIForInvalidInput(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), APIBase: server.URL}

	if _, err := client.Resolve(context.Background(), "v1.2.3", "amd64"); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("invalid version error = %v", err)
	}
	if _, err := client.Resolve(context.Background(), "helper-v1.2.3", "386"); !errors.Is(err, ErrUnsupportedArch) {
		t.Fatalf("invalid arch error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want 0", requests.Load())
	}
}

func TestResolveAPIErrorsAreRedacted(t *testing.T) {
	t.Parallel()
	const secret = "SECRET-RESPONSE-BODY"
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "not found", status: http.StatusNotFound, body: secret, want: ErrReleaseNotFound},
		{name: "server error", status: http.StatusInternalServerError, body: secret, want: ErrAPI},
		{name: "rate limit", status: http.StatusForbidden, body: secret, want: ErrAPI},
		{name: "invalid json", status: http.StatusOK, body: secret, want: ErrAPI},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			client := &Client{HTTP: server.Client(), APIBase: server.URL}
			_, err := client.Resolve(context.Background(), "helper-v1.2.3", "amd64")
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaks response body: %v", err)
			}
		})
	}
}

func TestResolveRejectsOversizedAPIResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Length", strconv.FormatInt(maxAPIResponse+1, 10))
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), APIBase: server.URL}
	_, err := client.Resolve(context.Background(), "helper-v1.2.3", "amd64")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
}

func TestResolveEnforcesProductionAssetPolicy(t *testing.T) {
	t.Parallel()
	assets := testAssets("https://evil.example", "helper-v1.2.3", "amd64")
	body, err := json.Marshal(apiRelease{TagName: "helper-v1.2.3", Assets: assets})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{
		APIBase: defaultAPIBase,
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return response(http.StatusOK, string(body)), nil
		})},
	}
	_, err = client.Resolve(context.Background(), "helper-v1.2.3", "amd64")
	if !errors.Is(err, ErrURLRejected) {
		t.Fatalf("error = %v, want ErrURLRejected", err)
	}
}

func TestClientSupportsControlledTestOrigin(t *testing.T) {
	t.Parallel()
	const base = "http://api.example.test"
	body, err := json.Marshal(apiRelease{
		TagName: "helper-v1.2.3",
		Assets:  testAssets(base, "helper-v1.2.3", "amd64"),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{
		APIBase: base,
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/asset" {
				return response(http.StatusOK, "asset"), nil
			}
			return response(http.StatusOK, string(body)), nil
		})},
	}
	release, err := client.Resolve(context.Background(), "helper-v1.2.3", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if release.ArchiveURL != base+"/download/"+release.ArchiveName {
		t.Fatalf("archive URL = %q", release.ArchiveURL)
	}
	content, err := client.Download(context.Background(), base+"/asset", 1024)
	if err != nil || string(content) != "asset" {
		t.Fatalf("Download() = %q, %v", content, err)
	}
}

func TestResolveFallsBackToPublishedReleaseList(t *testing.T) {
	t.Parallel()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/releases/tags/") {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(t, writer, []apiRelease{{
			TagName: "helper-v1.2.3",
			Assets:  testAssets(server.URL, "helper-v1.2.3", "amd64"),
		}})
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), APIBase: server.URL}
	release, err := client.Resolve(context.Background(), "helper-v1.2.3", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "helper-v1.2.3" {
		t.Fatalf("version = %q", release.Version)
	}
}

func TestLatestPaginatesAndSelectsMaximumSemVer(t *testing.T) {
	t.Parallel()
	var server *httptest.Server
	var pages atomic.Int32
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/releases" {
			t.Errorf("path = %q", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		pages.Add(1)
		if request.URL.Query().Get("per_page") != strconv.Itoa(apiPageSize) {
			t.Errorf("per_page = %q", request.URL.Query().Get("per_page"))
		}
		page, err := strconv.Atoi(request.URL.Query().Get("page"))
		if err != nil {
			t.Errorf("page = %q", request.URL.Query().Get("page"))
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		var releases []apiRelease
		switch page {
		case 1:
			for index := 0; index < apiPageSize; index++ {
				version := fmt.Sprintf("helper-v1.0.%d", index)
				releases = append(releases, apiRelease{
					TagName: version,
					Assets:  testAssets(server.URL, version, "arm64"),
				})
			}
		case 2:
			releases = []apiRelease{{
				TagName: "helper-v2.0.0-rc.1",
				Assets:  testAssets(server.URL, "helper-v2.0.0-rc.1", "arm64"),
			}}
		default:
			t.Errorf("unexpected page %d", page)
		}
		writeJSON(t, writer, releases)
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client(), APIBase: server.URL}
	release, err := client.Latest(context.Background(), "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "helper-v2.0.0-rc.1" {
		t.Fatalf("Latest() version = %q", release.Version)
	}
	if pages.Load() != 2 {
		t.Fatalf("pages = %d, want 2", pages.Load())
	}
}

func TestLatestSkipsDraftsMalformedTagsAndUnsuitableAssets(t *testing.T) {
	t.Parallel()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		duplicate := testAssets(server.URL, "helper-v7.0.0", "amd64")
		duplicate = append(duplicate, duplicate[0])
		incomplete := testAssets(server.URL, "helper-v8.0.0", "amd64")
		incomplete = incomplete[1:]
		releases := []apiRelease{
			{TagName: "helper-v9.0.0", Draft: true, Assets: testAssets(server.URL, "helper-v9.0.0", "amd64")},
			{TagName: "nightly", Assets: testAssets(server.URL, "nightly", "amd64")},
			{TagName: "helper-v8.0.0", Assets: incomplete},
			{TagName: "helper-v7.0.0", Assets: duplicate},
			{TagName: "helper-v6.0.0", Assets: testAssets(server.URL, "helper-v6.0.0", "amd64")},
			{TagName: "helper-v6.1.0-rc.1", Assets: testAssets(server.URL, "helper-v6.1.0-rc.1", "amd64")},
			{TagName: "helper-v6.2.0", Assets: testAssets("https://evil.example", "helper-v6.2.0", "amd64")},
			{TagName: "helper-v5.0.0", Assets: testAssets(server.URL, "helper-v5.0.0", "amd64")},
		}
		writeJSON(t, writer, releases)
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client(), APIBase: server.URL}
	release, err := client.Latest(context.Background(), "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "helper-v6.1.0-rc.1" {
		t.Fatalf("Latest() version = %q", release.Version)
	}
}

func TestLatestReportsAPIErrorInsteadOfNoUpdate(t *testing.T) {
	t.Parallel()
	const secret = "SECRET-API-BODY"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(secret))
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), APIBase: server.URL}
	_, err := client.Latest(context.Background(), "amd64")
	if !errors.Is(err, ErrAPI) {
		t.Fatalf("error = %v, want ErrAPI", err)
	}
	if errors.Is(err, ErrReleaseNotFound) {
		t.Fatalf("API error was reported as no release: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks response body: %v", err)
	}
}

func TestLatestReportsNoReleaseOnlyWhenListIsEmpty(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(t, writer, []apiRelease{})
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), APIBase: server.URL}
	if _, err := client.Latest(context.Background(), "amd64"); !errors.Is(err, ErrReleaseNotFound) {
		t.Fatalf("error = %v, want ErrReleaseNotFound", err)
	}
}

func TestDownloadBounded(t *testing.T) {
	t.Parallel()
	content := []byte("verified asset bytes")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(content)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), APIBase: server.URL}
	url := server.URL + "/asset"

	got, err := client.Download(context.Background(), url, int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("Download() = %q", got)
	}
	if _, err := client.Download(context.Background(), url, int64(len(content)-1)); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("small bound error = %v", err)
	}
	if _, err := client.Download(context.Background(), url, 0); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("zero bound error = %v", err)
	}
}

func TestDownloadEnforcesActualSizeWithoutContentLength(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("x", 32)
	client := &Client{
		APIBase: "http://127.0.0.1:12345",
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			result := response(http.StatusOK, content)
			result.ContentLength = -1
			return result, nil
		})},
	}
	if _, err := client.Download(context.Background(), "http://127.0.0.1:12345/asset", 16); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
}

func TestDownloadProductionPolicy(t *testing.T) {
	t.Parallel()
	githubURL := "https://github.com/suridler/home-router/releases/download/helper-v1.2.3/xray-core-helper-v1.2.3-linux-amd64.tar.gz"
	tests := []struct {
		name string
		url  string
		want error
	}{
		{name: "allowed github asset", url: githubURL},
		{name: "http rejected", url: "http://github.com/suridler/home-router/releases/download/helper-v1.2.3/archive", want: ErrURLRejected},
		{name: "other repository", url: "https://github.com/other/repository/releases/download/helper-v1.2.3/archive", want: ErrURLRejected},
		{name: "other host", url: "https://evil.example/suridler/home-router/releases/download/helper-v1.2.3/archive", want: ErrURLRejected},
		{name: "userinfo", url: "https://user@github.com/suridler/home-router/releases/download/helper-v1.2.3/archive", want: ErrURLRejected},
		{name: "explicit port", url: "https://github.com:443/suridler/home-router/releases/download/helper-v1.2.3/archive", want: ErrURLRejected},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			client := &Client{
				APIBase: defaultAPIBase,
				HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					calls.Add(1)
					return response(http.StatusOK, "asset"), nil
				})},
			}
			_, err := client.Download(context.Background(), test.url, 1024)
			if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("error = %v, want %v", err, test.want)
				}
				if calls.Load() != 0 {
					t.Fatalf("transport called %d times for rejected URL", calls.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("transport calls = %d, want 1", calls.Load())
			}
		})
	}
}

func TestDownloadAllowsGitHubUserContentRedirect(t *testing.T) {
	t.Parallel()
	initial := "https://github.com/suridler/home-router/releases/download/helper-v1.2.3/archive"
	var calls atomic.Int32
	client := &Client{
		APIBase: defaultAPIBase,
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			switch request.URL.Host {
			case "github.com":
				return redirectResponse("https://release-assets.githubusercontent.com/asset"), nil
			case "release-assets.githubusercontent.com":
				return response(http.StatusOK, "asset"), nil
			default:
				t.Errorf("unexpected host %q", request.URL.Host)
				return response(http.StatusForbidden, ""), nil
			}
		})},
	}
	content, err := client.Download(context.Background(), initial, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "asset" || calls.Load() != 2 {
		t.Fatalf("content = %q, calls = %d", content, calls.Load())
	}
}

func TestDownloadRejectsArbitraryRedirectOrigin(t *testing.T) {
	t.Parallel()
	initial := "https://github.com/suridler/home-router/releases/download/helper-v1.2.3/archive"
	var calls atomic.Int32
	client := &Client{
		APIBase: defaultAPIBase,
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			if request.URL.Host == "github.com" {
				return redirectResponse("https://evil.example/asset"), nil
			}
			return response(http.StatusOK, "asset"), nil
		})},
	}
	if _, err := client.Download(context.Background(), initial, 1024); !errors.Is(err, ErrURLRejected) {
		t.Fatalf("error = %v, want ErrURLRejected", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want 1", calls.Load())
	}
}

func TestDownloadTestModeRejectsCrossOriginRedirect(t *testing.T) {
	t.Parallel()
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Error("cross-origin target must not be contacted")
	}))
	defer target.Close()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/asset", http.StatusFound)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), APIBase: server.URL}
	if _, err := client.Download(context.Background(), server.URL+"/asset", 1024); !errors.Is(err, ErrURLRejected) {
		t.Fatalf("error = %v, want ErrURLRejected", err)
	}
}

func TestDownloadErrorsAreRedactedAndCancellationIsVisible(t *testing.T) {
	t.Parallel()
	const secret = "SECRET-ASSET-BODY"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(secret))
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), APIBase: server.URL}
	_, err := client.Download(context.Background(), server.URL+"/asset", 1024)
	if !errors.Is(err, ErrDownload) {
		t.Fatalf("error = %v, want ErrDownload", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks response body: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Download(ctx, server.URL+"/asset", 1024); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Download() error = %v", err)
	}
}

func TestResolveHistoricalBundleWithoutRawCLI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assets := testAssets("http://unused.test", "helper-v1.0.0", "amd64")
		var kept []apiAsset
		for _, asset := range assets {
			if strings.HasSuffix(asset.Name, ".tar.gz") || asset.Name == "SHA256SUMS" {
				asset.BrowserDownloadURL = "http://" + r.Host + "/" + asset.Name
				kept = append(kept, asset)
			}
		}
		writeJSON(t, w, apiRelease{TagName: "helper-v1.0.0", Assets: kept})
	}))
	defer server.Close()
	c := &Client{HTTP: server.Client(), APIBase: server.URL}
	release, err := c.Resolve(context.Background(), "helper-v1.0.0", "amd64")
	if err != nil || release.ArchiveURL == "" || release.CLIURL != "" {
		t.Fatalf("historical bundle: %#v %v", release, err)
	}
}
