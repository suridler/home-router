package helperapp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestInProcessTransportReinitializesAfterSetupCompletion(t *testing.T) {
	ctx := context.Background()
	completed := make(chan struct{})
	setupClosed := make(chan struct{}, 1)
	normalClosed := make(chan struct{}, 1)
	builds := 0
	build := func(ctx context.Context, configPath, version string) (*inProcessState, error) {
		builds++
		if builds == 1 {
			return &inProcessState{
				handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/setup/apply" {
						t.Errorf("setup handler path = %q", r.URL.Path)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"status":"configured_direct"}`)
					close(completed)
				}),
				close:          func() { setupClosed <- struct{}{} },
				setupCompleted: completed,
			}, nil
		}
		return &inProcessState{
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/templates/tproxy-unit/backups" {
					t.Errorf("normal handler path = %q", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"generation":7,"backups":[]}`)
			}),
			close: func() { normalClosed <- struct{}{} },
		}, nil
	}
	transport := newInProcessTransport(ctx, "/tmp/helper.toml", "test", build)
	if err := transport.start(ctx); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport}
	response, err := client.Post("http://helper/v1/setup/apply", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != `{"status":"configured_direct"}` {
		t.Fatalf("setup response = %d %q", response.StatusCode, body)
	}
	if builds != 2 {
		t.Fatalf("builds after apply = %d; want reinitialized normal composition", builds)
	}
	response, err = client.Get("http://helper/v1/templates/tproxy-unit/backups")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"generation":7`) {
		t.Fatalf("normal response = %d %q", response.StatusCode, body)
	}
	transport.Close()
	transport.Close()
	if len(setupClosed) != 1 {
		t.Fatalf("setup close calls = %d; want one", len(setupClosed))
	}
	if len(normalClosed) != 1 {
		t.Fatalf("normal close calls = %d; want one", len(normalClosed))
	}
}

func TestInProcessTransportDeliversSetupResponseWhenReinitializationFails(t *testing.T) {
	ctx := context.Background()
	completed := make(chan struct{})
	setupClosed := make(chan struct{}, 1)
	builds := 0
	build := func(ctx context.Context, configPath, version string) (*inProcessState, error) {
		builds++
		if builds == 1 {
			return &inProcessState{
				handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.WriteString(w, `{"status":"configured_direct"}`)
					close(completed)
				}),
				close:          func() { setupClosed <- struct{}{} },
				setupCompleted: completed,
			}, nil
		}
		return nil, errors.New("normal composition failed")
	}
	transport := newInProcessTransport(ctx, "/tmp/helper.toml", "test", build)
	if err := transport.start(ctx); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport}
	response, err := client.Post("http://helper/v1/setup/apply", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("setup response error = %v; want successful apply response", err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != `{"status":"configured_direct"}` {
		t.Fatalf("setup response = %d %q", response.StatusCode, body)
	}
	if _, err = client.Get("http://helper/v1/templates"); err == nil || !strings.Contains(err.Error(), "normal composition failed") {
		t.Fatalf("next request error = %v; want reinitialization failure", err)
	}
	transport.Close()
	if len(setupClosed) != 1 {
		t.Fatalf("setup close calls = %d; want one", len(setupClosed))
	}
}
