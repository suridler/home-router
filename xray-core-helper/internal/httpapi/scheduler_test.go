package httpapi

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/safeapply"
	"home-router/xray-core-helper/internal/scheduler"
	"home-router/xray-core-helper/internal/xrayconfig"
	"home-router/xray-core-helper/internal/xrayservice"
)

func TestScheduledJobsPreserveAPIGates(t *testing.T) {
	for _, gate := range []string{"open", "pending", "paths_changed"} {
		t.Run(gate, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "helper.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			errors := &errorsFake{}
			domains := &domainReconcileFake{}
			updates := &httpUpdateFake{}
			deps := ServerServices{Errors: errors, DomainReconcile: domains, Executor: updates}
			if gate == "pending" {
				pending := &applyTemplateFake{transaction: transactionFixture()}
				pending.transaction.Phase = safeapply.PhaseApplied
				deps.ApplyTemplate = pending
			}
			server := NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}), nil, nil, deps)
			expected := xrayservice.Installation{Binary: "/expected"}
			server.Handler = xrayservice.Guard(server.Handler, expected, func(context.Context) (xrayservice.Installation, error) {
				if gate == "paths_changed" {
					return xrayservice.Installation{}, nil
				}
				return expected, nil
			})
			done := make(chan struct{})
			go func() { defer close(done); _ = server.Serve(listener) }()
			defer func() { _ = server.Close(); <-done }()
			jobs := append(scheduler.MaintenanceJobs(path), scheduler.GeodataJob(path, 4, 0))
			for _, job := range jobs {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				err := job.Run(ctx, time.Date(2026, 10, 18, 4, 0, 0, 0, time.Local))
				cancel()
				blocked := gate == "paths_changed" || (gate == "pending" && job.Name != "errors")
				if (err != nil) != blocked {
					t.Fatalf("%s gate %s: %v", job.Name, gate, err)
				}
			}
			// Shutdown joins handlers before inspecting the fake services.
			if err := server.Shutdown(context.Background()); err != nil && err != http.ErrServerClosed {
				t.Fatal(err)
			}
			wantErrors, wantWrites := 1, 1
			if gate == "paths_changed" {
				wantErrors = 0
			}
			if gate != "open" {
				wantWrites = 0
			}
			if errors.collected != wantErrors || domains.calls != wantWrites || updates.calls != wantWrites {
				t.Fatal(errors.collected, domains.calls, updates)
			}
		})
	}
}
