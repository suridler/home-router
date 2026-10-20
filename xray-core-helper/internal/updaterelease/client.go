package updaterelease

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIBase   = "https://api.github.com/repos/suridler/home-router"
	defaultTimeout   = 30 * time.Second
	githubAssetRoot  = "/suridler/home-router/releases/download/"
	maxAPIResponse   = 8 << 20
	apiPageSize      = 100
	maxAPIPages      = 100
	maxHTTPRedirects = 5
	userAgent        = "xray-core-helper-updaterelease"
)

var (
	// ErrUnsupportedArch reports an architecture other than linux amd64/arm64.
	ErrUnsupportedArch = errors.New("unsupported release architecture")
	// ErrInvalidRelease reports missing, duplicate or inconsistent release assets.
	ErrInvalidRelease = errors.New("invalid release metadata")
	// ErrReleaseNotFound reports that no published release matches the request.
	ErrReleaseNotFound = errors.New("release not found")
	// ErrAPI reports a GitHub API failure. Response bodies are never included.
	ErrAPI = errors.New("release API request failed")
	// ErrURLRejected reports a download URL outside the fixed GitHub policy.
	ErrURLRejected = errors.New("download URL rejected")
	// ErrDownload reports a failed bounded asset download.
	ErrDownload = errors.New("release download failed")
	// ErrResponseTooLarge reports a response above the caller's size bound.
	ErrResponseTooLarge = errors.New("response exceeds size limit")
	// ErrInvalidArgument reports an unusable client, context or size bound.
	ErrInvalidArgument = errors.New("invalid release client argument")
)

// Release identifies the assets of one published helper release.
type Release struct {
	Version      string `json:"version"`
	ArchiveName  string `json:"archiveName"`
	ArchiveURL   string `json:"archiveUrl"`
	ChecksumsURL string `json:"checksumsUrl"`
	CLIName      string `json:"cliName"`
	CLIURL       string `json:"cliUrl"`
}

// Client resolves and downloads releases from the fixed home-router GitHub
// repository. APIBase may be replaced only by tests or controlled callers; asset
// URLs are still restricted to the configured origin, and the default client
// keeps the production GitHub policy.
type Client struct {
	HTTP    *http.Client
	APIBase string
}

// NewClient returns a production client with a bounded request timeout.
func NewClient() *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: defaultTimeout},
		APIBase: defaultAPIBase,
	}
}

type apiAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type apiRelease struct {
	TagName string     `json:"tag_name"`
	Draft   bool       `json:"draft"`
	Assets  []apiAsset `json:"assets"`
}

// Resolve returns the exact published, non-draft tag and its required assets.
func (client *Client) Resolve(ctx context.Context, version, arch string) (Release, error) {
	if err := ValidateVersion(version); err != nil {
		return Release{}, err
	}
	if err := validateArch(arch); err != nil {
		return Release{}, err
	}
	base, err := client.baseURL()
	if err != nil {
		return Release{}, err
	}
	endpoint, err := joinEndpoint(base, "/releases/tags/"+url.PathEscape(version), "")
	if err != nil {
		return Release{}, err
	}
	body, status, err := client.fetch(ctx, endpoint, maxAPIResponse, client.apiRedirectAllowed, "application/vnd.github+json")
	if err != nil {
		return Release{}, apiError(err)
	}
	if status == http.StatusNotFound {
		return client.resolveFromList(ctx, base, version, arch)
	}
	if status != http.StatusOK {
		return Release{}, ErrAPI
	}
	var release apiRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return Release{}, ErrAPI
	}
	if release.Draft || release.TagName != version {
		return Release{}, ErrReleaseNotFound
	}
	return client.releaseFromAssets(version, arch, release.Assets)
}

func (client *Client) resolveFromList(ctx context.Context, base *url.URL, version, arch string) (Release, error) {
	for page := 1; page <= maxAPIPages; page++ {
		endpoint, err := joinEndpoint(base, "/releases", url.Values{
			"per_page": {strconv.Itoa(apiPageSize)},
			"page":     {strconv.Itoa(page)},
		}.Encode())
		if err != nil {
			return Release{}, err
		}
		body, status, err := client.fetch(ctx, endpoint, maxAPIResponse, client.apiRedirectAllowed, "application/vnd.github+json")
		if err != nil {
			return Release{}, apiError(err)
		}
		if status == http.StatusNotFound {
			return Release{}, ErrReleaseNotFound
		}
		if status != http.StatusOK {
			return Release{}, ErrAPI
		}
		var releases []apiRelease
		if err := json.Unmarshal(body, &releases); err != nil {
			return Release{}, ErrAPI
		}
		for _, release := range releases {
			if release.TagName != version {
				continue
			}
			if release.Draft {
				return Release{}, ErrReleaseNotFound
			}
			return client.releaseFromAssets(version, arch, release.Assets)
		}
		if len(releases) < apiPageSize {
			break
		}
		if page == maxAPIPages {
			return Release{}, ErrAPI
		}
	}
	return Release{}, ErrReleaseNotFound
}

// Latest returns the greatest published semantic version with assets suitable
// for arch. Drafts, malformed tags and releases without the required assets are
// skipped; publication dates are not considered.
func (client *Client) Latest(ctx context.Context, arch string) (Release, error) {
	if err := validateArch(arch); err != nil {
		return Release{}, err
	}
	base, err := client.baseURL()
	if err != nil {
		return Release{}, err
	}
	var best Release
	var bestTag string
	for page := 1; page <= maxAPIPages; page++ {
		endpoint, err := joinEndpoint(base, "/releases", url.Values{
			"per_page": {strconv.Itoa(apiPageSize)},
			"page":     {strconv.Itoa(page)},
		}.Encode())
		if err != nil {
			return Release{}, err
		}
		body, status, err := client.fetch(ctx, endpoint, maxAPIResponse, client.apiRedirectAllowed, "application/vnd.github+json")
		if err != nil {
			return Release{}, apiError(err)
		}
		if status != http.StatusOK {
			return Release{}, ErrAPI
		}
		var releases []apiRelease
		if err := json.Unmarshal(body, &releases); err != nil {
			return Release{}, ErrAPI
		}
		if len(releases) == 0 {
			break
		}
		for _, release := range releases {
			if release.Draft || ValidateVersion(release.TagName) != nil {
				continue
			}
			candidate, err := client.releaseFromAssets(release.TagName, arch, release.Assets)
			if err != nil {
				continue
			}
			if bestTag == "" {
				best = candidate
				bestTag = release.TagName
				continue
			}
			comparison, err := Compare(release.TagName, bestTag)
			if err != nil || comparison <= 0 {
				continue
			}
			best = candidate
			bestTag = release.TagName
		}
		if len(releases) < apiPageSize {
			break
		}
		if page == maxAPIPages {
			return Release{}, ErrAPI
		}
	}
	if bestTag == "" {
		return Release{}, ErrReleaseNotFound
	}
	return best, nil
}

// Download retrieves a bounded asset from an allowed GitHub or configured test
// origin. Redirects are checked with the same origin policy.
func (client *Client) Download(ctx context.Context, rawURL string, max int64) ([]byte, error) {
	if err := client.validateDownloadURL(rawURL); err != nil {
		return nil, err
	}
	if max <= 0 {
		return nil, ErrInvalidArgument
	}
	body, status, err := client.fetch(ctx, rawURL, max, client.downloadRedirectAllowed, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, ErrDownload
	}
	return body, nil
}

func apiError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.Join(ErrAPI, err)
}

func validateArch(arch string) error {
	switch arch {
	case "amd64", "arm64":
		return nil
	default:
		return ErrUnsupportedArch
	}
}

func assetNames(version, arch string) (archive, cli, checksums string) {
	suffix := strings.TrimPrefix(version, "helper-")
	archive = "xray-core-helper-" + suffix + "-linux-" + arch + ".tar.gz"
	cli = strings.TrimSuffix(archive, ".tar.gz")
	return archive, cli, "SHA256SUMS"
}

func (client *Client) releaseFromAssets(version, arch string, assets []apiAsset) (Release, error) {
	archiveName, cliName, checksumsName := assetNames(version, arch)
	urls := make(map[string]string, len(assets))
	for _, asset := range assets {
		if asset.Name == "" {
			continue
		}
		if _, exists := urls[asset.Name]; exists {
			return Release{}, ErrInvalidRelease
		}
		urls[asset.Name] = asset.BrowserDownloadURL
	}
	// A historical compatible bundle can be installed with the saved current
	// installer even when that release predates standalone raw CLI assets.
	names := []string{archiveName, checksumsName}
	if urls[cliName] != "" {
		names = append(names, cliName)
	}
	for _, name := range names {
		if _, exists := urls[name]; !exists || urls[name] == "" {
			return Release{}, ErrInvalidRelease
		}
	}
	for _, name := range names {
		if err := client.validateDownloadURL(urls[name]); err != nil {
			return Release{}, err
		}
	}
	return Release{
		Version:      version,
		ArchiveName:  archiveName,
		ArchiveURL:   urls[archiveName],
		ChecksumsURL: urls[checksumsName],
		CLIName:      cliName,
		CLIURL:       urls[cliName],
	}, nil
}

func (client *Client) apiBase() string {
	if client == nil {
		return defaultAPIBase
	}
	value := strings.TrimSpace(client.APIBase)
	if value == "" {
		return defaultAPIBase
	}
	return value
}

func (client *Client) productionMode() bool {
	return strings.TrimRight(client.apiBase(), "/") == defaultAPIBase
}

func (client *Client) baseURL() (*url.URL, error) {
	parsed, err := url.Parse(client.apiBase())
	if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, ErrInvalidArgument
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, ErrInvalidArgument
	}
	parsed.RawPath = ""
	return parsed, nil
}

func joinEndpoint(base *url.URL, routePath, rawQuery string) (string, error) {
	endpoint := *base
	endpoint.Path = strings.TrimRight(base.Path, "/") + routePath
	endpoint.RawPath = ""
	endpoint.RawQuery = rawQuery
	endpoint.Fragment = ""
	return endpoint.String(), nil
}

func (client *Client) fetch(ctx context.Context, rawURL string, max int64, redirect func(*url.URL) error, accept string) ([]byte, int, error) {
	if ctx == nil || client == nil || max <= 0 {
		return nil, 0, ErrInvalidArgument
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, ErrInvalidArgument
	}
	request.Header.Set("User-Agent", userAgent)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}

	httpClient := client.clientWithRedirectCheck(redirect)
	response, err := httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		if errors.Is(err, ErrURLRejected) {
			return nil, 0, ErrURLRejected
		}
		return nil, 0, ErrDownload
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, response.StatusCode, nil
	}
	if response.ContentLength > max {
		return nil, response.StatusCode, ErrResponseTooLarge
	}
	limit := max
	if max < math.MaxInt64 {
		limit = max + 1
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, ErrDownload
	}
	if int64(len(body)) > max {
		return nil, response.StatusCode, ErrResponseTooLarge
	}
	return body, response.StatusCode, nil
}

func (client *Client) clientWithRedirectCheck(check func(*url.URL) error) *http.Client {
	base := &http.Client{Timeout: defaultTimeout}
	if client != nil && client.HTTP != nil {
		base = client.HTTP
	}
	clone := *base
	previous := base.CheckRedirect
	clone.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > maxHTTPRedirects {
			return ErrURLRejected
		}
		if check != nil {
			if err := check(request.URL); err != nil {
				return ErrURLRejected
			}
		}
		if previous != nil {
			return previous(request, via)
		}
		return nil
	}
	return &clone
}

func (client *Client) apiRedirectAllowed(target *url.URL) error {
	base, err := client.baseURL()
	if err != nil {
		return ErrURLRejected
	}
	if !sameOrigin(target, base) {
		return ErrURLRejected
	}
	if target.User != nil || target.Fragment != "" {
		return ErrURLRejected
	}
	return nil
}

func (client *Client) downloadRedirectAllowed(target *url.URL) error {
	if client.productionMode() {
		if !githubRedirectURL(target) {
			return ErrURLRejected
		}
		return nil
	}
	base, err := client.baseURL()
	if err != nil {
		return ErrURLRejected
	}
	if !sameOrigin(target, base) || target.User != nil || target.Fragment != "" {
		return ErrURLRejected
	}
	return nil
}

func (client *Client) validateDownloadURL(rawURL string) error {
	if client == nil {
		return ErrInvalidArgument
	}
	target, err := url.Parse(rawURL)
	if err != nil || target.Scheme == "" || target.Host == "" ||
		target.User != nil || target.Fragment != "" {
		return ErrURLRejected
	}
	if client.productionMode() {
		if !githubAssetURL(target) {
			return ErrURLRejected
		}
		return nil
	}
	base, err := client.baseURL()
	if err != nil {
		return ErrInvalidArgument
	}
	if !sameOrigin(target, base) {
		return ErrURLRejected
	}
	return nil
}

func sameOrigin(first, second *url.URL) bool {
	return strings.EqualFold(first.Scheme, second.Scheme) &&
		strings.EqualFold(first.Host, second.Host)
}

func githubAssetURL(target *url.URL) bool {
	if !strings.EqualFold(target.Scheme, "https") ||
		!strings.EqualFold(target.Hostname(), "github.com") ||
		target.Port() != "" || target.User != nil {
		return false
	}
	cleaned := path.Clean(target.Path)
	if !strings.HasPrefix(cleaned, githubAssetRoot) {
		return false
	}
	return strings.TrimPrefix(cleaned, githubAssetRoot) != ""
}

func githubRedirectURL(target *url.URL) bool {
	if !strings.EqualFold(target.Scheme, "https") ||
		target.Port() != "" || target.User != nil || target.Fragment != "" {
		return false
	}
	host := strings.ToLower(target.Hostname())
	if host == "github.com" {
		return githubAssetURL(target)
	}
	return host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com")
}
