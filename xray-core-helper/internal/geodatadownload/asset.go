package geodatadownload

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/geodatalimits"
)

type AssetFailure struct {
	Stage string
	Cause error
}

func (e *AssetFailure) Error() string { return e.Stage + ": " + e.Cause.Error() }
func (e *AssetFailure) Unwrap() error { return e.Cause }

type HTTPStatusError struct{ Status int }

func (e *HTTPStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Status) }
func (e *HTTPStatusError) Unwrap() error { return ErrDownload }

// FetchAsset independently downloads and verifies one fixed external asset.
// Its failure never discards the other file's result.
func (d *Downloader) FetchAsset(ctx context.Context, cfg bootstrap.GeodataConfig, name string) ([]byte, error) {
	if ctx == nil {
		return nil, ErrInvalidConfiguration
	}
	if err := cfg.Validate(); err != nil || !geodatalimits.ValidMB(cfg.MaxAssetSizeMB) {
		return nil, ErrInvalidConfiguration
	}
	source := cfg.Geosite
	switch name {
	case "geosite.dat":
	case "geoip.dat":
		source = cfg.Geoip
	default:
		return nil, ErrInvalidConfiguration
	}
	if source.URL == "" || source.Checksum.URL == "" {
		return nil, ErrSourcesDisabled
	}
	ctx, cancel := context.WithTimeout(ctx, d.overallTimeout)
	defer cancel()
	limit := int64(geodatalimits.Bytes(cfg.MaxAssetSizeMB))
	if d.maxAssetSize > 0 {
		limit = min(limit, d.maxAssetSize)
	}
	content, err := d.fetch(ctx, cfg, source.URL, limit)
	if err != nil {
		if errors.Is(err, ErrResponseTooLarge) {
			err = errors.Join(err, &geodatalimits.Exceeded{LimitBytes: int(limit)})
		}
		return nil, err
	}
	checksum, err := d.fetch(ctx, cfg, source.Checksum.URL, maximumChecksumSize)
	if err != nil {
		return nil, &AssetFailure{Stage: "checksum_download", Cause: err}
	}
	expected, err := parseChecksum(checksum, name)
	if err != nil {
		return nil, &AssetFailure{Stage: "checksum", Cause: err}
	}
	actual := sha256.Sum256(content)
	if subtle.ConstantTimeCompare(expected, actual[:]) != 1 {
		return nil, &AssetFailure{Stage: "checksum", Cause: ErrChecksumMismatch}
	}
	return content, nil
}
