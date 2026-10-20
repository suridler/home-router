package scheduler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"home-router/xray-core-helper/internal/unixhttp"
)

// Requests use the same Unix-socket API as manual commands, preserving service
// path checks, pending safe-apply gates and the domain mutation coordinator.
// No CLI subprocess or systemd oneshot is involved.
func MaintenanceJobs(socket string) []Job {
	client := unixhttp.NewClient(socket, 20*time.Minute)
	return []Job{
		{Name: "errors", Next: Every(20 * time.Second), Timeout: 15 * time.Second,
			Run: func(ctx context.Context, _ time.Time) error { return post(ctx, client, "/v1/errors/collect", "{}", "") }},
		{Name: "routing", Next: Every(30 * time.Second), Timeout: 25 * time.Second,
			Run: func(ctx context.Context, _ time.Time) error {
				return post(ctx, client, "/v1/proxy-domains/reconcile", "{}", "")
			}},
	}
}

func GeodataJob(socket string, hour, minute int) Job {
	client := unixhttp.NewClient(socket, 20*time.Minute)
	return Job{Name: "geodata", Next: Daily(hour, minute), Timeout: 20 * time.Minute,
		Run: func(ctx context.Context, due time.Time) error {
			return post(ctx, client, "/v1/xray/geodata/update", "", "scheduled-geodata-"+due.Format("2006-01-02-1504"))
		}}
}

func post(ctx context.Context, client *http.Client, path, body, key string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://helper"+path, bytes.NewBufferString(body))
	if err != nil {
		return errors.New("scheduled_request_unavailable")
	}
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("scheduled_request_unavailable")
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("scheduled_request_failed")
	}
	return nil
}
