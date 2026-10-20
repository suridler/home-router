package nativebinding

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestPrepareTypedPlaceholdersAndConcreteRoundTrip(t *testing.T) {
	for _, tc := range []struct{ kind, provider, statement, value, expected string }{
		{"interface", "setup.interface", `define device = ${lan_interface} # keep`, "br-test", `define device = "br-test" # keep`},
		{"ipv4-prefix", "setup.ipv4-prefix", `define prefix = ${lan_network}`, "10.0.0.0/24", `define prefix = 10.0.0.0/24`},
		{"port", "xray.tproxy-port", "native \\\n  tproxy to :${tproxy_port}", "61000", "native \\\n  tproxy to :61000"},
		{"uint32", "helper.network.intercept-mark", `Environment=TPROXY_MARK=${intercept_mark}`, "2", `Environment=TPROXY_MARK=2`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			raw := []byte("# unchanged\r\n# helper:v1 bind selected type=" + tc.kind + " source=" + tc.provider + "\n" + tc.statement + "\n# trailing\n")
			if _, err := Parse(raw); err == nil {
				t.Fatal("unresolved template accepted as installed document")
			}
			resolver := func(source string) (string, error) {
				if source != tc.provider {
					t.Fatal(source)
				}
				return tc.value, nil
			}
			got, err := Prepare(raw, resolver)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(got, []byte(tc.expected)) || !bytes.HasPrefix(got, []byte("# unchanged\r\n")) || !bytes.HasSuffix(got, []byte("# trailing\n")) {
				t.Fatalf("unexpected rendering: %s", got)
			}
			doc, err := Parse(got)
			if err != nil {
				t.Fatal(err)
			}
			again, err := doc.Render(resolver)
			if err != nil || !bytes.Equal(got, again) {
				t.Fatalf("concrete round trip: %v", err)
			}
			unchanged, err := Prepare(got, nil)
			if err != nil || !bytes.Equal(got, unchanged) {
				t.Fatal("concrete preparation changed bytes", err)
			}
		})
	}
}

func TestPrepareRejectsAmbiguousOrUnsafePlaceholders(t *testing.T) {
	for _, statement := range []string{`define x = "${value}"`, `define x = prefix${value}`, `define x = ${value}suffix`, `# ${value}`, `define x = 1 # ${value}`, `define x = ${value} + ${other}`, `define x = 1`, `define x = $${value}`} {
		t.Run(statement, func(t *testing.T) {
			raw := []byte("# helper:v1 bind selected type=uint32 source=helper.network.intercept-mark\n" + statement + "\n")
			if _, err := Prepare(raw, func(string) (string, error) { return "1", nil }); err == nil {
				t.Fatal("accepted invalid scalar")
			}
		})
	}
	for _, value := range []string{"eth0; reboot", "eth0\nanything", `a"b`, "..", "0123456789012345"} {
		raw := []byte("# helper:v1 bind selected type=interface source=setup.interface\ndefine x = ${value}\n")
		if _, err := Prepare(raw, func(string) (string, error) { return value, nil }); err == nil {
			t.Fatal("unsafe interface accepted")
		}
	}
	raw := []byte("# helper:v1 bind selected type=port source=xray.tproxy-port\ndefine x = ${value}\n")
	if _, err := Prepare(raw, func(string) (string, error) { return "", errors.New("secret") }); !errors.Is(err, ErrUnresolved) {
		t.Fatal(err)
	}
	if _, err := Prepare(append(raw, raw...), func(string) (string, error) { return "60000", nil }); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err := Prepare([]byte(strings.ReplaceAll(string(raw), "xray.tproxy-port", "unknown.source")), nil); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestPrepareDoesNotExpandNativeEnvironmentReferences(t *testing.T) {
	raw := []byte("[Service]\nExecStart=/usr/sbin/ip rule add fwmark ${TPROXY_MARK}\n")
	got, err := Prepare(raw, nil)
	if err != nil || !bytes.Equal(raw, got) {
		t.Fatal("native environment reference changed", err)
	}
}
