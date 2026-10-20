package setup

import (
	"context"
	"net"
	"testing"
)

type networkFixture struct {
	items []net.Interface
	addrs map[string][]net.Addr
}

func (f networkFixture) Interfaces() ([]net.Interface, error)      { return f.items, nil }
func (f networkFixture) Addrs(i net.Interface) ([]net.Addr, error) { return f.addrs[i.Name], nil }

type routesFixture map[string]bool

func (f routesFixture) Defaults(context.Context) (map[string]bool, error) { return f, nil }

type addr string

func (a addr) Network() string { return "ip" }
func (a addr) String() string  { return string(a) }
func TestDiscoveryIncludesExcludedAndAmbiguousRoutes(t *testing.T) {
	n := networkFixture{[]net.Interface{{Name: "docker0", Flags: net.FlagUp}, {Name: "lan0", Flags: net.FlagUp}, {Name: "wan0", Flags: net.FlagUp}, {Name: "lo", Flags: net.FlagUp | net.FlagLoopback}}, map[string][]net.Addr{"lan0": {addr("192.0.2.1/24")}, "wan0": {}}}
	d := Discoverer{links: n, routes: routesFixture{"lan0": true, "wan0": true}}
	got, e := d.Discover(context.Background())
	if e != nil || len(got) != 4 || got[0].Name != "lan0" || !got[0].DefaultRoute || !got[1].DefaultRoute || !got[2].Excluded || !got[3].Excluded {
		t.Fatalf("got=%#v err=%v", got, e)
	}
}
