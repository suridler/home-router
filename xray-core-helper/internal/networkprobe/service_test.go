package networkprobe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
)

type resolveFunc func(context.Context, string, string) ([]net.IP, error)

func (f resolveFunc) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	return f(ctx, network, host)
}

type runFunc func(context.Context, string, []string) ([]byte, error)

func (f runFunc) Run(ctx context.Context, path string, args []string) ([]byte, error) {
	return f(ctx, path, args)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixture(t *testing.T, resolver Resolver, runner Runner, trip roundTripFunc) (*Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network-probes.json")
	if resolver == nil {
		resolver = resolveFunc(func(_ context.Context, network, host string) ([]net.IP, error) {
			if network != "ip4" || host != "www.google.com" {
				t.Errorf("unexpected DNS target: %s %s", network, host)
			}
			return []net.IP{net.ParseIP("142.251.150.119")}, nil
		})
	}
	if runner == nil {
		runner = runFunc(func(_ context.Context, path string, args []string) ([]byte, error) {
			if path == "/sbin/ip" {
				return []byte(`[{"gateway":"192.168.1.1","dev":"eth0"}]`), nil
			}
			if path != "/usr/bin/ping" || strings.Join(args, " ") != "-4 -n -c 1 -W 2 -- 192.168.1.1" {
				t.Errorf("unsafe command: %s %v", path, args)
			}
			return nil, nil
		})
	}
	if trip == nil {
		trip = func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 204, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
	}
	s, err := newService(path, func() bootstrap.ConnectivityConfig { return bootstrap.Defaults().Connectivity }, resolver, runner, func(ip string) http.RoundTripper {
		if ip != "142.251.150.119" {
			t.Errorf("unresolved target: %s", ip)
		}
		return trip
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestProbePersistsBeforeAfterWithoutClaimingLAN(t *testing.T) {
	s, path := fixture(t, nil, nil, nil)
	before, err := s.Probe(context.Background(), api.NetworkProbeRequest{Stage: "before"})
	if err != nil || !before.Reachable || before.Gateway != "192.168.1.1" || before.LANForwarding != "requires_client_test" || before.CheckedAt.IsZero() {
		t.Fatalf("%+v %v", before, err)
	}
	after, err := s.Probe(context.Background(), api.NetworkProbeRequest{Stage: "after", TransactionID: "tx-1"})
	if err != nil || !after.Reachable {
		t.Fatal(after, err)
	}
	loaded, err := newService(path, s.settings, s.resolver, s.runner, s.transport)
	if err != nil {
		t.Fatal(err)
	}
	history := loaded.History()
	if history.Before.Stage != "before" || history.After.TransactionID != "tx-1" || history.Latest.Stage != "after" {
		t.Fatal(history)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("evidence permissions: %o", info.Mode().Perm())
	}
	if _, err = s.Probe(context.Background(), api.NetworkProbeRequest{Stage: "before"}); err != nil || s.History().After != nil {
		t.Fatal("new baseline retained stale after result", err)
	}
}

func TestPingFailureIsAdvisoryAndHTTPStatusMustMatch(t *testing.T) {
	for _, status := range []int{204, 200, 302, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := 0
			s, _ := fixture(t, nil, runFunc(func(_ context.Context, path string, _ []string) ([]byte, error) {
				if path == "/sbin/ip" {
					return []byte(`[{"gateway":"10.0.0.1","dev":"eth0"}]`), nil
				}
				return nil, errors.New("ping denied secret")
			}), func(r *http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://private.example/secret"}}, Body: io.NopCloser(strings.NewReader("secret")), Request: r}, nil
			})
			result, err := s.Probe(context.Background(), api.NetworkProbeRequest{Stage: "manual"})
			if err != nil || result.Reachable != (status == 204) || result.GatewayPing.Status != "failed" || result.HTTPStatus != status || requests != 1 {
				t.Fatal(result, err)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "secret") {
				t.Fatal("raw error/body/header leaked")
			}
		})
	}
}

func TestDNSRejectsNonPublicAddressesBeforeCommandsOrHTTP(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "198.18.0.1", "192.0.2.1", "::1", "240.0.0.1"} {
		t.Run(address, func(t *testing.T) {
			s, _ := fixture(t, resolveFunc(func(context.Context, string, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("142.251.150.119"), net.ParseIP(address)}, nil
			}), runFunc(func(context.Context, string, []string) ([]byte, error) {
				t.Error("commands run after unsafe DNS")
				return nil, nil
			}), func(*http.Request) (*http.Response, error) {
				t.Error("HTTP run after unsafe DNS")
				return nil, errors.New("unexpected")
			})
			result, err := s.Probe(context.Background(), api.NetworkProbeRequest{Stage: "manual"})
			if err != nil || result.Reachable || result.DNS.Code != "dns_non_public_ipv4" {
				t.Fatal(result, err)
			}
		})
	}
}

func TestFailedDNSAndTLSResultsAreSavedAndRedacted(t *testing.T) {
	for _, dnsFailure := range []bool{true, false} {
		resolver := resolveFunc(func(context.Context, string, string) ([]net.IP, error) {
			if dnsFailure {
				return nil, context.DeadlineExceeded
			}
			return []net.IP{net.ParseIP("142.251.150.119")}, nil
		})
		s, path := fixture(t, resolver, nil, func(*http.Request) (*http.Response, error) { return nil, errors.New("TLS failure credential SECRET") })
		result, err := s.Probe(context.Background(), api.NetworkProbeRequest{Stage: "manual"})
		if err != nil || result.Reachable {
			t.Fatal(result, err)
		}
		raw, _ := os.ReadFile(path)
		if strings.Contains(string(raw), "SECRET") {
			t.Fatal("secret leaked")
		}
		if dnsFailure && result.HTTPS.Status != "not_measured" {
			t.Fatal(result)
		}
	}
}

func TestConcurrentProbeReturnsBusyInsteadOfQueuing(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s, _ := fixture(t, resolveFunc(func(context.Context, string, string) ([]net.IP, error) {
		close(entered)
		<-release
		return nil, errors.New("DNS failed")
	}), nil, nil)
	done := make(chan error, 1)
	go func() { _, err := s.Probe(context.Background(), api.NetworkProbeRequest{Stage: "manual"}); done <- err }()
	<-entered
	_, err := s.Probe(context.Background(), api.NetworkProbeRequest{Stage: "manual"})
	close(release)
	if !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTransportDoesNotBypassRoutingOrUseEnvironmentProxy(t *testing.T) {
	transport := pinnedTransport("142.251.150.119").(*http.Transport)
	if transport.Proxy != nil || transport.TLSClientConfig != nil || transport.DialTLSContext != nil || !transport.DisableKeepAlives || transport.MaxResponseHeaderBytes != 16<<10 {
		t.Fatal("unsafe probe transport")
	}
}

func TestTLSVerificationIsRequired(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("HTTP reached despite invalid TLS certificate") }))
	server.Config.ErrorLog = nil
	server.StartTLS()
	defer server.Close()
	s, _ := fixture(t, nil, nil, nil)
	s.transport = func(string) http.RoundTripper {
		transport := pinnedTransport("142.251.150.119").(*http.Transport)
		// Only this hermetic test redirects the dial to a local TLS fixture.
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", server.Listener.Addr().String())
		}
		return transport
	}
	result, err := s.Probe(context.Background(), api.NetworkProbeRequest{Stage: "manual"})
	if err != nil || result.Reachable || result.HTTPS.Code != "tls_failed" {
		t.Fatal(result, err)
	}
}

func TestTransportTimeoutIsClassifiedBeforeOverallDeadline(t *testing.T) {
	s, _ := fixture(t, nil, nil, func(*http.Request) (*http.Response, error) {
		return nil, &net.DNSError{Err: "timeout", Name: "secret.example", IsTimeout: true}
	})
	result, err := s.Probe(context.Background(), api.NetworkProbeRequest{Stage: "manual"})
	if err != nil || result.HTTPS.Code != "tcp_timed_out" {
		t.Fatal(result, err)
	}
}

func TestRenameFollowedBySyncFailureDoesNotLoseCommittedEvidence(t *testing.T) {
	s, path := fixture(t, nil, nil, nil)
	s.persist = func(path string, history api.NetworkProbeHistory) (bool, error) {
		committed, err := save(path, history)
		if err != nil {
			return committed, err
		}
		return true, errors.New("directory sync failed")
	}
	if _, err := s.Probe(context.Background(), api.NetworkProbeRequest{Stage: "before"}); err == nil {
		t.Fatal("uncertain durability reported success")
	}
	if s.History().Before == nil {
		t.Fatal("committed result lost from memory")
	}
	s.persist = save
	if _, err := s.Probe(context.Background(), api.NetworkProbeRequest{Stage: "after", TransactionID: "tx-1"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := newService(path, s.settings, s.resolver, s.runner, s.transport)
	if err != nil || loaded.History().Before == nil || loaded.History().After == nil {
		t.Fatal("subsequent probe lost committed evidence", err)
	}
}
