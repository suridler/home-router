package scheduler

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGeodataRequestsKeepStableDailyKeyAndRedactFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	keys := make(chan string, 3)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/xray/geodata/update" {
			t.Error(r.Method, r.URL.Path)
		}
		keys <- r.Header.Get("Idempotency-Key")
		http.Error(w, "SECRET", 503)
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-done }()
	job := GeodataJob(path, 4, 0)
	due := time.Date(2026, 10, 18, 4, 0, 0, 0, time.Local)
	for _, at := range []time.Time{due, due, due.AddDate(0, 0, 1)} {
		err := job.Run(context.Background(), at)
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal(err)
		}
	}
	a, b, c := <-keys, <-keys, <-keys
	if a != b || a == c || a != "scheduled-geodata-2026-10-18-0400" {
		t.Fatal(a, b, c)
	}
}
