package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHelperClientFetchesTypedMetadataOverUnixSocket(t *testing.T) {
	client := serveHelperTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != helperMetadataPath {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"api":"127.0.0.1:10085",
			"inboundTags":["tproxy-in"],
			"outboundTags":["proxy","direct"],
			"generation":7,
			"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"modifiedAt":"2026-10-09T12:00:00Z",
			"futureField":{"nested":true}
		}`))
	}, time.Second)

	metadata, err := client.FetchMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.API != "127.0.0.1:10085" || metadata.Generation != 7 || metadata.ContentHash != strings.Repeat("a", 64) {
		t.Fatalf("metadata = %#v", metadata)
	}
	if strings.Join(metadata.InboundTags, ",") != "tproxy-in" || strings.Join(metadata.OutboundTags, ",") != "proxy,direct" {
		t.Fatalf("metadata tags = %#v, %#v", metadata.InboundTags, metadata.OutboundTags)
	}
}

func TestHelperClientReturnsStableTimeout(t *testing.T) {
	client := serveHelperTest(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}, 20*time.Millisecond)

	_, err := client.FetchMetadata(context.Background())
	if !errors.Is(err, ErrHelperTimeout) {
		t.Fatalf("error = %v; want timeout", err)
	}
}

func TestHelperClientRejectsIncompatibleResponses(t *testing.T) {
	validHash := strings.Repeat("b", 64)
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{`},
		{name: "missing tags", body: `{"api":"127.0.0.1:10085","generation":1,"contentHash":"` + validHash + `","modifiedAt":"2026-10-09T12:00:00Z"}`},
		{name: "invalid api", body: `{"api":"https://127.0.0.1:10085","inboundTags":[],"outboundTags":[],"generation":1,"contentHash":"` + validHash + `","modifiedAt":"2026-10-09T12:00:00Z"}`},
		{name: "invalid hash", body: `{"api":"127.0.0.1:10085","inboundTags":[],"outboundTags":[],"generation":1,"contentHash":"secret","modifiedAt":"2026-10-09T12:00:00Z"}`},
		{name: "duplicate tag", body: `{"api":"127.0.0.1:10085","inboundTags":["same","same"],"outboundTags":[],"generation":1,"contentHash":"` + validHash + `","modifiedAt":"2026-10-09T12:00:00Z"}`},
		{name: "trailing json", body: `{"api":"127.0.0.1:10085","inboundTags":[],"outboundTags":[],"generation":1,"contentHash":"` + validHash + `","modifiedAt":"2026-10-09T12:00:00Z"}{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := serveHelperTest(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}, time.Second)
			_, err := client.FetchMetadata(context.Background())
			if !errors.Is(err, ErrIncompatibleHelperResponse) {
				t.Fatalf("error = %v; want incompatible response", err)
			}
		})
	}
}

func serveHelperTest(t *testing.T, handler http.HandlerFunc, timeout time.Duration) *HelperClient {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "helper.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	serveDone := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(serveDone)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		<-serveDone
		_ = os.Remove(socketPath)
	})
	client, err := NewHelperClient(socketPath, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return client
}
