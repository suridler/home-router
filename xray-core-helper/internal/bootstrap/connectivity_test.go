package bootstrap

import "testing"

func TestConnectivityConfigurationRejectsUnsafeTargetsAndLimits(t *testing.T) {
	for _, value := range []string{"http://example.com/", "https://user:password@example.com/", "https://example.com/?token=secret", "https://example.com/#fragment", "https://example.com:8443/", "https:///path"} {
		config := Defaults().Connectivity
		config.URL = value
		if config.Validate() == nil {
			t.Errorf("accepted %s", value)
		}
	}
	for _, status := range []int{199, 300, 503} {
		config := Defaults().Connectivity
		config.ExpectedStatus = status
		if config.Validate() == nil {
			t.Errorf("accepted status %d", status)
		}
	}
	for _, timeout := range []int{0, 4, 31} {
		config := Defaults().Connectivity
		config.TimeoutSeconds = timeout
		if config.Validate() == nil {
			t.Errorf("accepted timeout %d", timeout)
		}
	}
	if err := Defaults().Connectivity.Validate(); err != nil {
		t.Fatal(err)
	}
}
