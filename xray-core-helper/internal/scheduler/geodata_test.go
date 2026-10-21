package scheduler

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
)

func TestConfiguredGeodataReadsScheduleOnEveryTick(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	socket := filepath.Join(t.TempDir(), "helper.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/xray/geodata/update" || r.Header.Get("Idempotency-Key") == "" {
			t.Error("wrong scheduled request")
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})}
	go server.Serve(listener)
	defer server.Close()
	cfg := bootstrap.SchedulerConfig{GeodataUpdateEnabled: false, GeodataUpdateTime: "04:00"}
	job := store.ConfiguredGeodata(socket, func(context.Context) (bootstrap.SchedulerConfig, error) { return cfg, nil })
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	if _, err := store.db.Exec("INSERT INTO helper_schedule(name,due) VALUES(?,?)", "geodata-04:00", now.Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := job.Run(ctx, now); err != nil || calls.Load() != 0 {
		t.Fatal("disabled job ran", err)
	}
	cfg.GeodataUpdateEnabled = true
	if err := job.Run(ctx, now); err != nil || calls.Load() != 1 {
		t.Fatal("saved deadline not run", err, calls.Load())
	}
	if err := job.Run(ctx, now.Add(time.Minute)); err != nil || calls.Load() != 1 {
		t.Fatal("duplicate daily execution", err, calls.Load())
	}
	cfg.GeodataUpdateTime = "05:30"
	if _, err := store.db.Exec("INSERT INTO helper_schedule(name,due) VALUES(?,?)", "geodata-05:30", now.Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := job.Run(ctx, now); err != nil || calls.Load() != 2 {
		t.Fatal("changed schedule not observed", err, calls.Load())
	}
}
