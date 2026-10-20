package setup

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"sort"
	"strings"
)

type Interface struct {
	Name         string   `json:"name"`
	Addresses    []string `json:"addresses"`
	DefaultRoute bool     `json:"defaultRoute"`
	Excluded     bool     `json:"excluded"`
	Reason       string   `json:"reason,omitempty"`
}
type interfaceSource interface {
	Interfaces() ([]net.Interface, error)
	Addrs(net.Interface) ([]net.Addr, error)
}
type routeSource interface {
	Defaults(context.Context) (map[string]bool, error)
}
type Discoverer struct {
	links  interfaceSource
	routes routeSource
}

func NewDiscoverer() *Discoverer {
	return &Discoverer{links: systemNetwork{}, routes: procRoutes{"/proc/net/route"}}
}
func (d *Discoverer) Discover(ctx context.Context) ([]Interface, error) {
	if d == nil || d.links == nil || d.routes == nil {
		return nil, errors.New("network discovery unavailable")
	}
	links, err := d.links.Interfaces()
	if err != nil {
		return nil, err
	}
	defaults, err := d.routes.Defaults(ctx)
	if err != nil {
		return nil, err
	}
	if len(links) > 128 {
		links = links[:128]
	}
	out := make([]Interface, 0, len(links))
	for _, link := range links {
		item := Interface{Name: link.Name, DefaultRoute: defaults[link.Name]}
		item.Excluded, item.Reason = excludedInterface(link)
		addrs, e := d.links.Addrs(link)
		if e != nil {
			return nil, e
		}
		for _, a := range addrs {
			if len(item.Addresses) == 16 {
				break
			}
			if ip, _, e := net.ParseCIDR(a.String()); e == nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				item.Addresses = append(item.Addresses, a.String())
			}
		}
		sort.Strings(item.Addresses)
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Excluded != out[j].Excluded {
			return !out[i].Excluded
		}
		if out[i].DefaultRoute != out[j].DefaultRoute {
			return out[i].DefaultRoute
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
func excludedInterface(link net.Interface) (bool, string) {
	if link.Flags&net.FlagLoopback != 0 || link.Name == "lo" {
		return true, "loopback"
	}
	if link.Flags&net.FlagUp == 0 {
		return true, "down"
	}
	for _, p := range []string{"docker", "br-", "veth", "virbr", "podman", "cni", "flannel", "tun", "tap", "wg"} {
		if strings.HasPrefix(link.Name, p) {
			return true, "virtual"
		}
	}
	if link.Flags&net.FlagUp == 0 {
		return true, "down"
	}
	return false, ""
}

type systemNetwork struct{}

func (systemNetwork) Interfaces() ([]net.Interface, error)      { return net.Interfaces() }
func (systemNetwork) Addrs(i net.Interface) ([]net.Addr, error) { return i.Addrs() }

type procRoutes struct{ path string }

func (p procRoutes) Defaults(ctx context.Context) (map[string]bool, error) {
	f, e := os.Open(p.path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	out := map[string]bool{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		v := strings.Fields(s.Text())
		if len(v) >= 4 && v[1] == "00000000" && v[3] != "00000000" {
			out[v[0]] = true
		}
	}
	return out, s.Err()
}
