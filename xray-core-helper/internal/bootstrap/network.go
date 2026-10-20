package bootstrap

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
)

// NetworkConfig owns operator-selected network parameters, never Xray settings.
type NetworkConfig struct {
	Interface     string `toml:"interface"`
	Address       string `toml:"address"`
	IPv4Prefix    string `toml:"ipv4_prefix"`
	InterceptMark uint32 `toml:"intercept_mark"`
	PolicyTable   uint32 `toml:"policy_table"`
}

func (n NetworkConfig) Validate() error {
	if n.InterceptMark == 0 || n.PolicyTable == 0 || n.PolicyTable >= 253 && n.PolicyTable <= 255 {
		return errors.New("network requires nonzero intercept_mark and a non-reserved policy_table")
	}
	if n.Interface == "" && n.Address == "" && n.IPv4Prefix == "" {
		return nil
	}
	a, err := netip.ParseAddr(n.Address)
	p, prefixErr := netip.ParsePrefix(n.IPv4Prefix)
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`).MatchString(n.Interface) || n.Interface == "." || n.Interface == ".." || err != nil || !a.Is4() || prefixErr != nil || !p.Addr().Is4() || p != p.Masked() || !p.Contains(a) {
		return errors.New("network requires an interface, IPv4 address and matching canonical prefix")
	}
	return nil
}

func (n NetworkConfig) BindingValue(source string) (string, error) {
	switch source {
	case "helper.network.interface":
		if n.Interface != "" {
			return n.Interface, nil
		}
	case "helper.network.ipv4-prefix":
		if n.IPv4Prefix != "" {
			return n.IPv4Prefix, nil
		}
	case "helper.network.address":
		if n.Address != "" {
			return n.Address, nil
		}
	case "helper.network.intercept-mark":
		return fmt.Sprint(n.InterceptMark), nil
	case "helper.network.policy-table":
		return fmt.Sprint(n.PolicyTable), nil
	}
	return "", errors.New("network_binding_unresolved")
}
