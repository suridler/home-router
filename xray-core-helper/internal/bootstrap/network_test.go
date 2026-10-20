package bootstrap

import "testing"

func TestNetworkDefaultsAndSelectionValidation(t *testing.T) {
	defaults := Defaults().Network
	if defaults.InterceptMark != 1 || defaults.PolicyTable != 100 || defaults.Validate() != nil {
		t.Fatal("invalid network defaults")
	}
	selected := defaults
	selected.Interface = "br-lan"
	selected.Address = "10.0.0.1"
	selected.IPv4Prefix = "10.0.0.0/24"
	if err := selected.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*NetworkConfig){
		func(n *NetworkConfig) { n.Interface = "" }, func(n *NetworkConfig) { n.Address = "10.1.0.1" }, func(n *NetworkConfig) { n.IPv4Prefix = "10.0.0.1/24" }, func(n *NetworkConfig) { n.PolicyTable = 254 }, func(n *NetworkConfig) { n.InterceptMark = 0 }, func(n *NetworkConfig) { n.Interface = "a;reboot" },
	} {
		n := selected
		mutate(&n)
		if n.Validate() == nil {
			t.Fatal("invalid network accepted")
		}
	}
	if _, err := defaults.BindingValue("helper.network.interface"); err == nil {
		t.Fatal("missing LAN silently resolved")
	}
}
