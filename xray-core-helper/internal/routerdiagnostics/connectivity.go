package routerdiagnostics

import (
	"context"
	"errors"
	"net"
	"time"
)

const ProbeTimeout = 3 * time.Second

type Layer struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Code      string    `json:"code"`
	CheckedAt time.Time `json:"checkedAt"`
	TimeoutMS int64     `json:"timeoutMs"`
	Scope     string    `json:"scope"`
}
type ConnectivityResult struct {
	Layers []Layer `json:"layers"`
}
type Connectivity struct {
	API      func(context.Context) error
	Outbound func(context.Context, string) (string, error)
	DNS      func(context.Context) error
	Dial     func(context.Context, string) error
}

func NewConnectivity(api func(context.Context) error, outbound func(context.Context, string) (string, error)) *Connectivity {
	return &Connectivity{API: api, Outbound: outbound, DNS: func(ctx context.Context) error {
		_, err := net.DefaultResolver.LookupIPAddr(ctx, "example.com")
		return err
	}, Dial: func(ctx context.Context, target string) error {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", target)
		if err == nil {
			conn.Close()
		}
		return err
	}}
}

// Probe is explicitly requested; it never mutates routing or accepts a target URL.
// Endpoint TCP is not a proxy protocol or end-to-end forwarding assertion.
func (c *Connectivity) Probe(ctx context.Context, tag string) (ConnectivityResult, error) {
	if c == nil || c.API == nil || c.Outbound == nil || c.DNS == nil || c.Dial == nil || len(tag) > 64 {
		return ConnectivityResult{}, errors.New("connectivity unavailable")
	}
	result := ConnectivityResult{}
	run := func(name, scope string, probe func(context.Context) error) {
		bounded, cancel := context.WithTimeout(ctx, ProbeTimeout)
		defer cancel()
		err := probe(bounded)
		layer := Layer{Name: name, Status: "ok", Code: name + "_ok", CheckedAt: time.Now().UTC(), TimeoutMS: ProbeTimeout.Milliseconds(), Scope: scope}
		if err != nil {
			layer.Status = "failed"
			layer.Code = name + "_failed"
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(bounded.Err(), context.DeadlineExceeded) {
				layer.Status = "timed_out"
				layer.Code = name + "_timed_out"
			}
		}
		result.Layers = append(result.Layers, layer)
	}
	run("xray_api", "helper_local", c.API)
	run("dns", "helper_resolver", c.DNS)
	if tag != "" {
		run("selected_outbound_tcp", "configured_endpoint_only", func(ctx context.Context) error {
			target, err := c.Outbound(ctx, tag)
			if err != nil {
				return err
			}
			return c.Dial(ctx, target)
		})
	}
	result.Layers = append(result.Layers, Layer{Name: "proxy_protocol", Status: "not_measured", Code: "proxy_handshake_not_measured", Scope: "requires_proxy_protocol_probe", CheckedAt: time.Now().UTC()}, Layer{Name: "lan_forwarding", Status: "requires_client_test", Code: "lan_client_test_required", Scope: "separate_lan_client", CheckedAt: time.Now().UTC()})
	return result, nil
}
