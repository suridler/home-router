// Package networkprobe checks the server's normal IPv4 path without changing
// routing, socket marks, proxy environment or the safe-apply decision.
package networkprobe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/atomicfile"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/commandrunner"
)

var ErrBusy = errors.New("network probe already running")

type Runner interface {
	Run(context.Context, string, []string) ([]byte, error)
}
type Resolver interface {
	LookupIP(context.Context, string, string) ([]net.IP, error)
}

type Service struct {
	mu        sync.RWMutex
	running   sync.Mutex
	path      string
	settings  func() bootstrap.ConnectivityConfig
	resolver  Resolver
	runner    Runner
	transport func(string) http.RoundTripper
	persist   func(string, api.NetworkProbeHistory) (bool, error)
	history   api.NetworkProbeHistory
}

func New(path string, settings func() bootstrap.ConnectivityConfig) (*Service, error) {
	return newService(path, settings, net.DefaultResolver, nativeRunner{}, pinnedTransport)
}

func newService(path string, settings func() bootstrap.ConnectivityConfig, resolver Resolver, runner Runner, transport func(string) http.RoundTripper) (*Service, error) {
	if !filepath.IsAbs(path) || settings == nil || settings().Validate() != nil || resolver == nil || runner == nil || transport == nil {
		return nil, errors.New("invalid network probe dependencies")
	}
	s := &Service{path: path, settings: settings, resolver: resolver, runner: runner, transport: transport, persist: save}
	file, err := os.Open(path)
	if err == nil {
		defer file.Close()
		decoder := json.NewDecoder(io.LimitReader(file, 32<<10))
		if decoder.Decode(&s.history) != nil {
			return nil, errors.New("invalid network probe evidence")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return nil, errors.New("invalid network probe evidence")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("network probe evidence unavailable")
	}
	return s, nil
}

func (s *Service) History() api.NetworkProbeHistory {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Results are immutable once published.
	return s.history
}

func (s *Service) Probe(ctx context.Context, request api.NetworkProbeRequest) (api.NetworkProbeResult, error) {
	if !s.running.TryLock() {
		return api.NetworkProbeResult{}, ErrBusy
	}
	defer s.running.Unlock()
	settings := s.settings()
	if settings.Validate() != nil {
		return api.NetworkProbeResult{}, errors.New("invalid network probe settings")
	}
	if request.Stage != "before" && request.Stage != "after" && request.Stage != "manual" {
		return api.NetworkProbeResult{}, errors.New("invalid network probe stage")
	}
	started := time.Now()
	result := api.NetworkProbeResult{Stage: request.Stage, TransactionID: request.TransactionID, CheckedAt: started.UTC(), URL: settings.URL, ExpectedStatus: settings.ExpectedStatus, LANForwarding: "requires_client_test",
		DNS:         api.NetworkProbeCheck{Status: "not_measured", Code: "dns_not_measured"},
		GatewayPing: api.NetworkProbeCheck{Status: "not_measured", Code: "gateway_unavailable"},
		HTTPS:       api.NetworkProbeCheck{Status: "not_measured", Code: "https_not_measured"}}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(settings.TimeoutSeconds)*time.Second)
	defer cancel()
	target, _ := url.Parse(settings.URL)
	dnsStart := time.Now()
	addresses, err := s.resolver.LookupIP(bounded, "ip4", target.Hostname())
	result.DNS.DurationMS = time.Since(dnsStart).Milliseconds()
	result.DNS.Status, result.DNS.Code = "ok", "dns_ok"
	if err != nil || len(addresses) == 0 {
		result.DNS.Status, result.DNS.Code = "failed", "dns_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			result.DNS.Code = "dns_timed_out"
		}
		if errors.Is(err, context.Canceled) {
			result.DNS.Code = "dns_canceled"
		}
	} else {
		for _, address := range addresses {
			if !publicIPv4(address) {
				result.DNS.Status, result.DNS.Code = "failed", "dns_non_public_ipv4"
				break
			}
		}
	}
	if result.DNS.Status == "ok" {
		result.RemoteIP = addresses[0].To4().String()
		s.gateway(bounded, &result)
		s.https(bounded, &result)
	}
	result.DurationMS = time.Since(started).Milliseconds()
	result.Reachable = result.HTTPS.Status == "ok"
	s.mu.RLock()
	next := s.history
	s.mu.RUnlock()
	next.Latest = &result
	if request.Stage == "before" {
		next.Before = &result
		next.After = nil
	}
	if request.Stage == "after" {
		next.After = &result
	}
	committed, err := s.persist(s.path, next)
	if committed {
		s.mu.Lock()
		s.history = next
		s.mu.Unlock()
	}
	if err != nil {
		return api.NetworkProbeResult{}, errors.New("network probe evidence could not be saved")
	}
	return result, nil
}

func (s *Service) gateway(ctx context.Context, result *api.NetworkProbeResult) {
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := s.runner.Run(bounded, "/sbin/ip", []string{"-4", "-j", "route", "get", result.RemoteIP})
	var routes []struct {
		Gateway string `json:"gateway"`
		Dev     string `json:"dev"`
	}
	if err != nil || json.Unmarshal(raw, &routes) != nil || len(routes) != 1 {
		return
	}
	address, err := netip.ParseAddr(routes[0].Gateway)
	if err != nil || !address.Is4() || address.IsUnspecified() || address.IsMulticast() || address.IsLoopback() {
		return
	}
	result.Gateway, result.Interface = address.String(), routes[0].Dev
	started := time.Now()
	_, err = s.runner.Run(bounded, "/usr/bin/ping", []string{"-4", "-n", "-c", "1", "-W", "2", "--", result.Gateway})
	result.GatewayPing = api.NetworkProbeCheck{Status: "ok", Code: "gateway_ping_ok", DurationMS: time.Since(started).Milliseconds()}
	if err != nil {
		result.GatewayPing.Status, result.GatewayPing.Code = "failed", "gateway_ping_failed"
	}
}

func (s *Service) https(ctx context.Context, result *api.NetworkProbeResult) {
	transport := s.transport(result.RemoteIP)
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		defer closer.CloseIdleConnections()
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, result.URL, nil)
	if err != nil {
		result.HTTPS = api.NetworkProbeCheck{Status: "failed", Code: "https_request_failed"}
		return
	}
	request.Header.Set("User-Agent", "xray-core-helper/network-probe")
	var phase atomic.Int32
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
		TLSHandshakeStart: func() { phase.Store(1) }, GotConn: func(httptrace.GotConnInfo) { phase.Store(2) },
	}))
	started := time.Now()
	response, err := client.Do(request)
	phaseName := []string{"tcp", "tls", "http"}[phase.Load()]
	result.HTTPS = api.NetworkProbeCheck{Status: "failed", Code: phaseName + "_failed", DurationMS: time.Since(started).Milliseconds()}
	if err != nil {
		var timeout net.Error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
			result.HTTPS.Code = phaseName + "_timed_out"
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			result.HTTPS.Code = phaseName + "_canceled"
		}
		return
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	result.HTTPS.Code = "unexpected_http_status"
	if response.StatusCode == result.ExpectedStatus {
		result.HTTPS.Status, result.HTTPS.Code = "ok", "https_ok"
	}
}

func pinnedTransport(ip string) http.RoundTripper {
	return &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 16 << 10, TLSHandshakeTimeout: 5 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp4", net.JoinHostPort(ip, "443"))
		}}
}

var excluded = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
}

func publicIPv4(ip net.IP) bool {
	ipv4 := ip.To4()
	if ipv4 == nil || !ipv4.IsGlobalUnicast() || ipv4.IsPrivate() {
		return false
	}
	address, _ := netip.AddrFromSlice(ipv4)
	for _, prefix := range excluded {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

type nativeRunner struct{}

func (nativeRunner) Run(ctx context.Context, executable string, args []string) ([]byte, error) {
	if executable != "/sbin/ip" && executable != "/usr/bin/ping" {
		return nil, errors.New("unsupported probe executable")
	}
	runner, err := commandrunner.NewWithOutputLimit(executable, 3*time.Second, 8<<10)
	if err != nil {
		return nil, err
	}
	result, err := runner.Run(ctx, args)
	return result.Stdout(), err
}

func save(path string, history api.NetworkProbeHistory) (bool, error) {
	encoded, err := json.Marshal(history)
	if err != nil {
		return false, err
	}
	return atomicfile.Write(path, encoded, func(f *os.File) error { return f.Chmod(0600) })
}
