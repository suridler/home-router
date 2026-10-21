package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	helperMetadataPath    = "/v1/xray/exporter-metadata"
	maxHelperResponseBody = 64 << 10
	maxMetadataTags       = 256
	maxMetadataTagBytes   = 256
)

var (
	ErrHelperUnavailable          = errors.New("xray-core-helper is unavailable")
	ErrHelperTimeout              = errors.New("xray-core-helper request timed out")
	ErrIncompatibleHelperResponse = errors.New("xray-core-helper metadata response is incompatible")
)

type HelperMetadata struct {
	API          string    `json:"api"`
	InboundTags  []string  `json:"inboundTags"`
	OutboundTags []string  `json:"outboundTags"`
	Generation   uint64    `json:"generation"`
	ContentHash  string    `json:"contentHash"`
	ModifiedAt   time.Time `json:"modifiedAt"`
}

type HelperClient struct {
	httpClient *http.Client
}

func NewHelperClient(socketPath string, timeout time.Duration) (*HelperClient, error) {
	if !strings.HasPrefix(socketPath, "/") || timeout <= 0 {
		return nil, fmt.Errorf("invalid helper client settings")
	}
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &HelperClient{httpClient: &http.Client{Transport: transport, Timeout: timeout}}, nil
}

func (client *HelperClient) FetchMetadata(ctx context.Context) (HelperMetadata, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://helper"+helperMetadataPath, nil)
	if err != nil {
		return HelperMetadata{}, ErrHelperUnavailable
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return HelperMetadata{}, ErrHelperTimeout
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return HelperMetadata{}, ErrHelperTimeout
		}
		return HelperMetadata{}, ErrHelperUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return HelperMetadata{}, ErrHelperUnavailable
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxHelperResponseBody+1))
	if err != nil || len(body) > maxHelperResponseBody {
		return HelperMetadata{}, ErrIncompatibleHelperResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var metadata HelperMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return HelperMetadata{}, ErrIncompatibleHelperResponse
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return HelperMetadata{}, ErrIncompatibleHelperResponse
	}
	if err := validateHelperMetadata(metadata); err != nil {
		return HelperMetadata{}, ErrIncompatibleHelperResponse
	}
	return metadata, nil
}

func validateHelperMetadata(metadata HelperMetadata) error {
	host, portText, err := net.SplitHostPort(metadata.API)
	if err != nil || host == "" {
		return errors.New("invalid Xray API address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid Xray API port")
	}
	hash, err := hex.DecodeString(metadata.ContentHash)
	if err != nil || len(hash) != 32 || metadata.ModifiedAt.IsZero() {
		return errors.New("invalid configuration metadata")
	}
	if metadata.InboundTags == nil || metadata.OutboundTags == nil {
		return errors.New("missing tag collections")
	}
	if err := validateMetadataTags(metadata.InboundTags); err != nil {
		return err
	}
	return validateMetadataTags(metadata.OutboundTags)
}

func validateMetadataTags(tags []string) error {
	if len(tags) > maxMetadataTags {
		return errors.New("too many tags")
	}
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if tag == "" || tag != strings.TrimSpace(tag) || len(tag) > maxMetadataTagBytes {
			return errors.New("invalid tag")
		}
		if _, exists := seen[tag]; exists {
			return errors.New("duplicate tag")
		}
		seen[tag] = struct{}{}
	}
	return nil
}
