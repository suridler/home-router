package helperapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/httpapi"
	"home-router/xray-core-helper/internal/setup"
)

type standaloneAdoptService struct {
	httpapi.SetupService
	adopted bool
}

func (s *standaloneAdoptService) Status() setup.ConfigProbe {
	return setup.ConfigProbe{State: setup.ConfigExisting, SetupRequired: !s.adopted, Fingerprint: "current"}
}

func (s *standaloneAdoptService) Adopt(_ context.Context, fingerprint string) (setup.Result, error) {
	if fingerprint != "current" {
		return setup.Result{}, errors.New("unexpected fingerprint")
	}
	s.adopted = true
	return setup.Result{Status: "configured_direct"}, nil
}

func TestInProcessSetupHandlesBodylessClientRequests(t *testing.T) {
	service := &standaloneAdoptService{}
	transport := newInProcessTransport(context.Background(), "unused", "test", func(context.Context, string, string) (*inProcessState, error) {
		return &inProcessState{handler: httpapi.NewSetupServer("test", service).Handler}, nil
	})
	if err := transport.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	client := &http.Client{Transport: transport}
	request, err := http.NewRequest(http.MethodGet, "http://helper/v1/setup", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Fingerprint string `json:"configFingerprint"`
	}
	err = json.NewDecoder(response.Body).Decode(&probe)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || probe.Fingerprint != "current" {
		t.Fatalf("bodyless setup GET failed: response=%+v err=%v", probe, err)
	}
	if request.Body != nil {
		t.Fatal("transport mutated the caller's request")
	}
	response, err = client.Post("http://helper/v1/setup/adopt", "application/json", strings.NewReader(`{"configFingerprint":"current"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !service.adopted {
		t.Fatalf("adoption failed: status=%d", response.StatusCode)
	}
}

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
