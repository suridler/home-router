package nativebinding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func marked(id, kind, source, token, statement string) string {
	quoted, _ := json.Marshal(token)
	return fmt.Sprintf("# helper:v1 bind %s type=%s source=%s token=%s\n%s\n", id, kind, source, quoted, statement)
}

func TestNativeFormatsAndRender(t *testing.T) {
	for _, tt := range []struct{ name, kind, source, token, statement, value, want string }{
		{"nft arbitrary name", "interface", "setup.interface", `"lan0"`, `define any_name = "lan0" # retain`, "br-test", `define any_name = "br-test" # retain`},
		{"sysctl arbitrary key", "bool01", "native.sysctl.other", "1", "custom.key = 1", "0", "custom.key = 0"},
		{"unit arbitrary command", "uint32", "xray.intercept-mark", "0x1", "ExecStart=/custom/tool --flag 0x1 other", "17", "ExecStart=/custom/tool --flag 0x11 other"},
		{"port after colon", "port", "xray.tproxy-port", "52345", "tproxy to :52345 accept", "23456", "tproxy to :23456 accept"},
		{"continuation", "port", "xray.tproxy-port", "52345", "native \\\n  tproxy to :52345 accept", "23456", "native \\\n  tproxy to :23456 accept"},
		{"prefix", "ipv4-prefix", "setup.ipv4-prefix", "192.0.2.0/24", "define arbitrary = 192.0.2.0/24", "198.51.100.0/25", "define arbitrary = 198.51.100.0/25"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte("# untouched\r\n" + marked("example", tt.kind, tt.source, tt.token, tt.statement) + "# trailing\n")
			doc, err := Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			resolver := func(source string) (string, error) {
				calls++
				if source != tt.source {
					t.Fatal("undeclared provider called")
				}
				return tt.value, nil
			}
			output, err := doc.Render(resolver)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !strings.Contains(string(output), tt.want) || !bytes.HasPrefix(output, []byte("# untouched\r\n")) || !bytes.HasSuffix(output, []byte("# trailing\n")) {
				t.Fatal("incorrect edit or provider calls")
			}
			updated, err := Parse(output)
			if err != nil {
				t.Fatal("saved locator cannot be read", err)
			}
			if err = updated.Check(resolver); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), tt.statement) {
				t.Fatal("input mutated")
			}
		})
	}
}

func TestUnannotatedIsUnchangedAndNeedsNoProviders(t *testing.T) {
	raw := []byte("# custom native content\nanything 999\n# helper:begin gateway\nmore arbitrary rules\n# helper:end gateway\n")
	doc, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Bindings()) != 0 || doc.Check(nil) != nil {
		t.Fatal("implicit declaration")
	}
	got, err := doc.Render(nil)
	if err != nil || !bytes.Equal(raw, got) {
		t.Fatal("unmarked bytes changed")
	}
}

func TestInvalidDeclarations(t *testing.T) {
	valid := marked("port", "port", "xray.tproxy-port", "52345", "native 52345")
	for index, raw := range []string{
		valid + valid,
		strings.Replace(valid, "helper:v1", "helper:v2", 1),
		strings.Replace(valid, "source=xray.tproxy-port", "source=arbitrary.command", 1),
		strings.Replace(valid, "type=port", "type=custom", 1),
		strings.Replace(valid, "native 52345", "native 52345 52345", 1),
		strings.Replace(valid, "native 52345", `native "52345"`, 1),
		strings.Replace(valid, "native 52345", "native 152345", 1),
		strings.Replace(valid, "native 52345", "native 52345 # helper:legacy", 1) + "# helper:legacy\n",
		strings.Split(valid, "\n")[0] + "\n",
		strings.Replace(valid, "\nnative", "\n\nnative", 1),
		marked("port", "port", "xray.tproxy-port", "65536", "native 65536"),
		marked("port", "port", "xray.tproxy-port", "-1", "native -1"),
		marked("name", "interface", "setup.interface", `"bad;command"`, `native "bad;command"`),
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("invalid declaration case %d accepted", index)
		}
	}
}

func TestCheckNumbersAndRedaction(t *testing.T) {
	doc, err := Parse([]byte(marked("mark", "uint32", "xray.egress-mark", "0xff", "native 0xff")))
	if err != nil {
		t.Fatal(err)
	}
	if err = doc.Check(func(string) (string, error) { return "255", nil }); err != nil {
		t.Fatal(err)
	}
	if err = doc.Check(func(string) (string, error) { return "1", nil }); !errors.Is(err, ErrMismatch) {
		t.Fatal(err)
	}
	secret := "SECRET_URI"
	if err = doc.Check(func(string) (string, error) { return "", errors.New(secret) }); !errors.Is(err, ErrUnresolved) || strings.Contains(err.Error(), secret) {
		t.Fatal("provider error leaked")
	}
	if _, err = doc.Render(func(string) (string, error) { return "-1", nil }); !errors.Is(err, ErrUnresolved) {
		t.Fatal("invalid provider accepted")
	}
}

func TestCrossDocumentReferences(t *testing.T) {
	nft, _ := Parse([]byte(marked("mark", "uint32", "xray.intercept-mark", "0x1", "define arbitrary=0x1")))
	unit, _ := Parse([]byte(marked("selected", "uint32", "native.nftables.mark", "1", "ExecStart=/tool --mark 1")))
	docs := map[string]*Document{"nftables": nft, "tproxy-unit": unit}
	checks, err := CheckDocuments(docs, func(string) (string, error) { return "1", nil })
	if err != nil || len(checks) != 2 || checks[0].Status != "ok" || checks[1].Status != "ok" {
		t.Fatal("equivalent marked values failed", err)
	}
	checks, err = CheckDocuments(docs, func(string) (string, error) { return "2", nil })
	if err != nil || checks[0].Status != "mismatch" || checks[1].Status != "ok" {
		t.Fatal("references must compare actual values")
	}
	cycle, _ := Parse([]byte(marked("mark", "uint32", "native.tproxy-unit.selected", "1", "native 1")))
	docs["nftables"] = cycle
	if _, err = CheckDocuments(docs, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("cycle accepted")
	}
	delete(docs, "nftables")
	checks, err = CheckDocuments(docs, nil)
	if err != nil || len(checks) != 1 || checks[0].Status != "unresolved" {
		t.Fatal("missing provider not unresolved")
	}
}

func TestBounds(t *testing.T) {
	if _, err := Parse(bytes.Repeat([]byte("x"), (1<<20)+1)); !errors.Is(err, ErrLimit) {
		t.Fatal("size bound missing")
	}
	var raw strings.Builder
	for i := 0; i < 257; i++ {
		raw.WriteString(marked(fmt.Sprintf("id%d", i), "port", "xray.tproxy-port", "1", "native 1"))
	}
	if _, err := Parse([]byte(raw.String())); !errors.Is(err, ErrLimit) {
		t.Fatal("declaration bound missing")
	}
}

func jsonGeositeMarked(id, token, statement string) string {
	quoted, _ := json.Marshal(token)
	return fmt.Sprintf("  // helper:v1 bind %s type=geosite-reference source=helper.user-geosite token=%s\n  %s\n", id, quoted, statement)
}

func TestJSONCGeositeReferenceRenderAndCheck(t *testing.T) {
	raw := []byte("{\n  // retained jsonc comment\n" + jsonGeositeMarked(
		"user_geosite",
		`"ext:geosite_user.dat:USERLIST"`,
		`"domain": ["ext:geosite_user.dat:USERLIST"] // retain USERLIST`,
	) + "}\n")
	doc, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Bindings()) != 1 || doc.Bindings()[0].Type != "geosite-reference" {
		t.Fatal("declaration not read", doc.Bindings())
	}
	if err = doc.Check(func(source string) (string, error) {
		if source != "helper.user-geosite" {
			t.Fatal("undeclared provider called")
		}
		return "geosite_user.dat", nil
	}); err != nil {
		t.Fatal("matching filename rejected", err)
	}
	checks, err := CheckDocuments(map[string]*Document{"xray": doc}, func(string) (string, error) {
		return "geosite_user.dat", nil
	})
	if err != nil || len(checks) != 1 || checks[0].Status != "ok" {
		t.Fatal("matching filename check failed", err, checks)
	}
	output, err := doc.Render(func(string) (string, error) { return "geosite_other.dat", nil })
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  // retained jsonc comment\n" + jsonGeositeMarked(
		"user_geosite",
		`"ext:geosite_other.dat:USERLIST"`,
		`"domain": ["ext:geosite_other.dat:USERLIST"] // retain USERLIST`,
	) + "}\n"
	if string(output) != want {
		t.Fatalf("unexpected render:\n%s", output)
	}
	if !bytes.Contains(output, []byte("USERLIST")) {
		t.Fatal("category not retained")
	}
	updated, err := Parse(output)
	if err != nil {
		t.Fatal("rendered locator cannot be read", err)
	}
	if err = updated.Check(func(string) (string, error) { return "geosite_other.dat", nil }); err != nil {
		t.Fatal("rendered filename does not match", err)
	}
	if err = updated.Check(func(string) (string, error) { return "geosite_user.dat", nil }); !errors.Is(err, ErrMismatch) {
		t.Fatal("stale filename accepted", err)
	}
	checks, err = CheckDocuments(map[string]*Document{"xray": updated}, func(string) (string, error) {
		return "geosite_other.dat", nil
	})
	if err != nil || len(checks) != 1 || checks[0].Status != "ok" {
		t.Fatal("rendered document check failed", err, checks)
	}
}

func TestGeositeReferenceRejectsUnsafeProviderFilename(t *testing.T) {
	doc, err := Parse([]byte(jsonGeositeMarked(
		"user_geosite",
		`"ext:geosite_user.dat:USERLIST"`,
		`"domain": ["ext:geosite_user.dat:USERLIST"]`,
	)))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"../geosite_user.dat",
		"ext:/geosite_user.dat:USERLIST",
		"geosite_user.dat/child",
		"geosite.dat",
	} {
		if _, err = doc.Render(func(string) (string, error) { return value, nil }); !errors.Is(err, ErrUnresolved) {
			t.Fatalf("unsafe render value %q accepted: %v", value, err)
		}
		if err = doc.Check(func(string) (string, error) { return value, nil }); !errors.Is(err, ErrUnresolved) {
			t.Fatalf("unsafe check value %q accepted: %v", value, err)
		}
	}
}

func TestGeositeReferenceInvalidLocators(t *testing.T) {
	for _, tt := range []struct {
		name      string
		token     string
		statement string
	}{
		{"missing category", `"ext:geosite_user.dat"`, `"domain": ["ext:geosite_user.dat"]`},
		{"path filename", `"ext:../geosite_user.dat:USERLIST"`, `"domain": ["ext:../geosite_user.dat:USERLIST"]`},
		{"path category", `"ext:geosite_user.dat:../USERLIST"`, `"domain": ["ext:geosite_user.dat:../USERLIST"]`},
		{"empty category", `"ext:geosite_user.dat:"`, `"domain": ["ext:geosite_user.dat:"]`},
		{"long category", `"ext:geosite_user.dat:` + strings.Repeat("A", 65) + `"`, `"domain": ["ext:geosite_user.dat:` + strings.Repeat("A", 65) + `"]`},
		{"invalid filename", `"ext:geosite.dat:USERLIST"`, `"domain": ["ext:geosite.dat:USERLIST"]`},
		{"unquoted locator", `ext:geosite_user.dat:USERLIST`, `"domain": ["ext:geosite_user.dat:USERLIST"]`},
		{"duplicate locator", `"ext:geosite_user.dat:USERLIST"`, `"domain": ["ext:geosite_user.dat:USERLIST", "ext:geosite_user.dat:USERLIST"]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(jsonGeositeMarked("user_geosite", tt.token, tt.statement))); err == nil {
				t.Fatal("invalid geosite locator accepted")
			}
		})
	}
}

func TestLiteralParameterCanBeSharedWithoutExternalProvider(t *testing.T) {
	root, err := Parse([]byte("# helper:v1 bind intercept type=uint32 source=native.literal token=\"0x42\"\ndefine interception = 0x42\n"))
	if err != nil {
		t.Fatal(err)
	}
	unit, err := Parse([]byte("# helper:v1 bind policy type=uint32 source=native.nftables.intercept token=\"66\"\nEnvironment=MARK=66\n"))
	if err != nil {
		t.Fatal(err)
	}
	checks, err := CheckDocuments(map[string]*Document{"nftables": root, "tproxy-unit": unit}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range checks {
		if check.Status != "ok" {
			t.Fatal(check)
		}
	}
}
