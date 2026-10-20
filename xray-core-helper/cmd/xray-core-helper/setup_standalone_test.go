package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type standaloneLifecycleFake struct {
	root                  bool
	events                []string
	stopHelperErr         error
	stopWebErr            error
	lockErr               error
	clientErr             error
	restartErr            error
	restartWithoutTimeout bool
	client                *http.Client
}

func (fake *standaloneLifecycleFake) event(value string) {
	fake.events = append(fake.events, value)
}

func (fake *standaloneLifecycleFake) IsRoot() bool { return fake.root }

func (fake *standaloneLifecycleFake) StopHelper(context.Context) error {
	fake.event("stop-helper")
	return fake.stopHelperErr
}

func (fake *standaloneLifecycleFake) StopWebIfInstalled(context.Context) error {
	fake.event("stop-web")
	return fake.stopWebErr
}

func (fake *standaloneLifecycleFake) AcquireRuntimeLock(context.Context) (func(), error) {
	fake.event("lock")
	if fake.lockErr != nil {
		return nil, fake.lockErr
	}
	return func() { fake.event("unlock") }, nil
}

func (fake *standaloneLifecycleFake) SetupClient(context.Context, string) (*http.Client, func(), error) {
	fake.event("client")
	if fake.clientErr != nil {
		return nil, nil, fake.clientErr
	}
	return fake.client, func() { fake.event("close-client") }, nil
}

func (fake *standaloneLifecycleFake) RestartHelper(ctx context.Context) error {
	fake.event("restart")
	if _, ok := ctx.Deadline(); !ok {
		fake.restartWithoutTimeout = true
	}
	return fake.restartErr
}

type setupRoundTripper func(*http.Request) (*http.Response, error)

func (transport setupRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func setupResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func successfulStandaloneClient(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: setupRoundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/setup/plan":
			return setupResponse(request, http.StatusOK, `{"plan":{"interface":"lan0","listenAddress":"192.0.2.1","lanNetwork":"192.0.2.0/24","routingEnabled":false,"artifacts":[]}}`), nil
		case "/v1/setup/apply":
			return setupResponse(request, http.StatusOK, `{"status":"configured_direct","interface":"lan0","routingEnabled":false}`), nil
		default:
			return nil, errors.New("unexpected standalone setup path")
		}
	})}
}

func eventIndex(events []string, value string) int {
	for index, event := range events {
		if event == value {
			return index
		}
	}
	return -1
}

func TestStandaloneSetupStopsServicesLocksThenRestartsAfterRelease(t *testing.T) {
	fake := &standaloneLifecycleFake{root: true, client: successfulStandaloneClient(t)}
	var stdout, stderr strings.Builder
	code := runStandaloneSetupWithLifecycle(
		[]string{"setup", "--interface", "lan0", "--non-interactive"},
		"/tmp/helper.toml", strings.NewReader(""), &stdout, &stderr, fake,
	)
	if code != 0 || !strings.Contains(stdout.String(), "configured_direct") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	want := []string{"stop-helper", "stop-web", "lock", "client", "close-client", "unlock", "restart"}
	if strings.Join(fake.events, ",") != strings.Join(want, ",") {
		t.Fatalf("events=%v; want %v", fake.events, want)
	}
	if eventIndex(fake.events, "unlock") > eventIndex(fake.events, "restart") {
		t.Fatalf("runtime lock was not released before daemon restart: %v", fake.events)
	}
	if fake.restartWithoutTimeout {
		t.Fatal("helper restart must use a bounded separate context")
	}
}

func TestStandaloneSetupRestartsHelperOnFailureAfterStop(t *testing.T) {
	client := &http.Client{Transport: setupRoundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/setup/plan":
			return setupResponse(request, http.StatusOK, `{"plan":{"interface":"lan0","listenAddress":"192.0.2.1","lanNetwork":"192.0.2.0/24","routingEnabled":false,"artifacts":[]}}`), nil
		default:
			return setupResponse(request, http.StatusServiceUnavailable, `{"error":{"code":"setup_failed","message":"SECRET"}}`), nil
		}
	})}
	fake := &standaloneLifecycleFake{root: true, client: client}
	var stdout, stderr strings.Builder
	code := runStandaloneSetupWithLifecycle(
		[]string{"setup", "--interface", "lan0", "--non-interactive"},
		"/tmp/helper.toml", strings.NewReader(""), &stdout, &stderr, fake,
	)
	if code == 0 {
		t.Fatalf("failure code = 0; stderr=%q", stderr.String())
	}
	if eventIndex(fake.events, "restart") < 0 || eventIndex(fake.events, "unlock") > eventIndex(fake.events, "restart") {
		t.Fatalf("failure did not restart helper after releasing the lock: %v", fake.events)
	}
	if strings.Contains(stderr.String(), "SECRET") {
		t.Fatalf("stderr leaked server detail: %q", stderr.String())
	}
	if fake.restartWithoutTimeout {
		t.Fatal("helper restart must use a bounded separate context")
	}
}

func TestStandaloneSetupRequiresRootBeforeStoppingServices(t *testing.T) {
	fake := &standaloneLifecycleFake{root: false}
	var stdout, stderr strings.Builder
	code := runStandaloneSetupWithLifecycle(
		[]string{"setup", "--non-interactive"}, "/tmp/helper.toml",
		strings.NewReader(""), &stdout, &stderr, fake,
	)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "requires root") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if len(fake.events) != 0 {
		t.Fatalf("lifecycle events before root check: %v", fake.events)
	}
}

func TestStandaloneSetupRestartsHelperWhenClientInitializationFails(t *testing.T) {
	fake := &standaloneLifecycleFake{root: true, clientErr: errors.New("private host detail")}
	var stdout, stderr strings.Builder
	code := runStandaloneSetupWithLifecycle(
		[]string{"setup", "--non-interactive"}, "/tmp/helper.toml",
		strings.NewReader(""), &stdout, &stderr, fake,
	)
	if code != 1 || strings.Contains(stderr.String(), "private host detail") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if eventIndex(fake.events, "unlock") < 0 || eventIndex(fake.events, "restart") < 0 {
		t.Fatalf("events=%v; want lock release and helper restart", fake.events)
	}
}

func TestStandaloneSetupStatusUsesOrdinarySocketPath(t *testing.T) {
	server := startUnixAPIServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/setup" {
			t.Errorf("path = %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"configured","configState":"existing"}`)
	}))
	configPath := writeCLIConfig(t, server.socket)
	fake := &standaloneLifecycleFake{root: false}
	var stdout, stderr strings.Builder
	code := runStandaloneSetupWithLifecycle(
		[]string{"setup", "--status"}, configPath,
		strings.NewReader(""), &stdout, &stderr, fake,
	)
	if code != 0 || !strings.Contains(stdout.String(), `"status": "configured"`) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if len(fake.events) != 0 {
		t.Fatalf("status-only setup touched lifecycle: %v", fake.events)
	}
}

func TestStandaloneStatusRequiresReadOnlyParsedFlags(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		readOnly bool
	}{
		{[]string{"setup", "--status"}, true},
		{[]string{"setup", "--status=true"}, true},
		{[]string{"setup", "--status=false"}, false},
		{[]string{"setup", "--status", "--status=false"}, false},
		{[]string{"setup", "--status", "--recover"}, false},
		{[]string{"setup", "--status", "--unknown"}, false},
	} {
		if got := standaloneStatusOnly(tc.args); got != tc.readOnly {
			t.Fatalf("%v: %v", tc.args, got)
		}
	}
}
