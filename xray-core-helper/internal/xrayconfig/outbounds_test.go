package xrayconfig

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/transaction"
)

const outboundFixture = `{
  // operator note stays
  "inbounds": [],
  "outbounds": [
    { "tag": "proxy", "protocol": "vless",
      "settings": { "vnext": [{ "address": "old.example", "port": 443, "users": [{ "id": "00000000-0000-4000-8000-000000000000" }] }] },
      "streamSettings": { "network": "tcp", "sockopt": { "mark": 255 }, "customKeep": true } },
    { "tag": "extra", "protocol": "vless", "unknownOutbound": 1,
      "settings": { "vnext": [] }, "streamSettings": { "method": "raw" } },
    { "tag": "direct", "protocol": "freedom" },
    { "tag": "dns-out", "protocol": "dns" },
    { "tag": "spare", "protocol": "freedom" },
    { "tag": "grp-1", "protocol": "freedom" }
  ],
  "routing": {
    "rules": [ { "ruleTag": "r1", "outboundTag": "extra", "domain": ["example.com"] } ],
    "balancers": [ { "tag": "bal", "selector": ["grp-"] } ]
  }
}`

const newURI = "vless://22222222-2222-4222-8222-222222222222@new.example:443?type=tcp&security=reality&pbk=k&sni=s.example&sid=ab"

func plan(t *testing.T, planner transaction.Planner, source string) (string, error) {
	t.Helper()
	edits, err := planner([]byte(source))
	if err != nil {
		return "", err
	}
	updated, err := transaction.ApplyEdits([]byte(source), edits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseSource(updated); err != nil {
		t.Fatalf("result no longer parses: %v\n%s", err, updated)
	}
	return string(updated), nil
}

func rejectCode(err error) string {
	code, _, _ := transaction.ErrorInfo(err)
	return code
}

func TestAddVlessOutboundSetsEgressMarkAndKeepsDocument(t *testing.T) {
	updated, err := plan(t, PlanAddVlessOutbound("edge-2", newURI), outboundFixture)
	if err != nil {
		t.Fatal(err)
	}
	e := newEditorT(t, updated)
	index, outbound := findOutbound(e.syntax, "edge-2")
	if index != 6 {
		t.Fatalf("new outbound index = %d", index)
	}
	stream := asMap(outbound["streamSettings"])
	if mark, _ := asMap(stream["sockopt"])["mark"].(interface{ String() string }); mark == nil || mark.String() != "255" {
		t.Fatalf("sockopt = %#v", stream["sockopt"])
	}
	for _, kept := range []string{"// operator note stays", `"unknownOutbound": 1`, `"customKeep": true`} {
		if !strings.Contains(updated, kept) {
			t.Errorf("lost %q", kept)
		}
	}
}

func TestAddVlessOutboundRejections(t *testing.T) {
	tests := []struct {
		name, tag, uri, code string
	}{
		{"duplicate", "extra", newURI, "outbound_exists"},
		{"reserved", "block", newURI, "outbound_protected"},
		{"bad tag", "bad tag!", newURI, "invalid_outbound_tag"},
		{"route collision", "vless", newURI, "invalid_outbound_tag"},
		{"bad uri", "edge-3", "https://x", "invalid_vless_uri"},
		{"unsupported", "edge-3", newURI + "&weird=1", "unsupported_vless_parameters"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := plan(t, PlanAddVlessOutbound(test.tag, test.uri), outboundFixture); rejectCode(err) != test.code {
				t.Fatalf("error = %v (%s); want %s", err, rejectCode(err), test.code)
			}
		})
	}
}

func TestUpdateVlessOutboundPreservesSockoptAndTransportKey(t *testing.T) {
	updated, err := plan(t, PlanUpdateVlessOutbound("proxy", newURI), outboundFixture)
	if err != nil {
		t.Fatal(err)
	}
	e := newEditorT(t, updated)
	_, outbound := findOutbound(e.syntax, "proxy")
	stream := asMap(outbound["streamSettings"])
	if stream["network"] != "tcp" || asMap(stream["sockopt"])["mark"] == nil {
		t.Fatalf("stream = %#v", stream)
	}
	if strings.Contains(updated, "old.example") || !strings.Contains(updated, "new.example") {
		t.Fatalf("settings not replaced:\n%s", updated)
	}
	if strings.Contains(updated, "customKeep") {
		t.Log("customKeep is replaced with streamSettings as in the Node importer")
	}
	methodUpdated, err := plan(t, PlanUpdateVlessOutbound("extra", newURI), outboundFixture)
	if err != nil {
		t.Fatal(err)
	}
	_, extra := findOutbound(newEditorT(t, methodUpdated).syntax, "extra")
	if asMap(extra["streamSettings"])["method"] != "raw" {
		t.Fatalf("method transport key not kept: %#v", extra["streamSettings"])
	}
	if !strings.Contains(methodUpdated, `"unknownOutbound": 1`) {
		t.Fatal("unknown outbound field lost")
	}
}

func TestUpdateVlessOutboundRejections(t *testing.T) {
	tests := []struct{ name, tag, code string }{
		{"missing", "ghost", "outbound_not_found"},
		{"not vless", "direct", "outbound_not_vless"},
		{"dns outbound", "dns-out", "outbound_not_vless"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := plan(t, PlanUpdateVlessOutbound(test.tag, newURI), outboundFixture); rejectCode(err) != test.code {
				t.Fatalf("error = %v; want %s", err, test.code)
			}
		})
	}
}

func TestDeleteOutbound(t *testing.T) {
	updated, err := plan(t, PlanDeleteOutbound("spare"), outboundFixture)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(updated, `"tag": "spare"`) || !strings.Contains(updated, "// operator note stays") {
		t.Fatalf("unexpected delete result:\n%s", updated)
	}
	tests := []struct{ name, tag, code string }{
		{"protected", "direct", "outbound_protected"},
		{"protected dns", "dns-out", "outbound_protected"},
		{"referenced by rule", "extra", "outbound_in_use"},
		{"referenced by balancer selector", "grp-1", "outbound_in_use"},
		{"missing", "ghost", "outbound_not_found"},
		{"invalid tag", "../x", "invalid_outbound_tag"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := plan(t, PlanDeleteOutbound(test.tag), outboundFixture); rejectCode(err) != test.code {
				t.Fatalf("error = %v (%s); want %s", err, rejectCode(err), test.code)
			}
		})
	}
}

func TestMissingOutboundsArrayIsRejected(t *testing.T) {
	_, err := plan(t, PlanAddVlessOutbound("edge-2", newURI), `{"routing":{}}`)
	if rejectCode(err) != "invalid_config" {
		t.Fatalf("error = %v", err)
	}
}

type acceptAll struct{}

func (acceptAll) Validate(context.Context, string) error { return nil }

func TestPlannerRunsInsideEngineTransaction(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte(outboundFixture), 0640); err != nil {
		t.Fatal(err)
	}
	store, err := transaction.OpenStore(context.Background(), filepath.Join(directory, "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	engine, err := transaction.NewEngine(configPath, store, acceptAll{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(configPath)

	_, err = engine.Mutate(context.Background(), 0, PlanDeleteOutbound("extra"), false)
	if rejectCode(err) != "outbound_in_use" {
		t.Fatalf("Mutate() error = %v", err)
	}
	after, _ := os.ReadFile(configPath)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected mutation changed the file")
	}
	if _, err := engine.Mutate(context.Background(), 5, PlanDeleteOutbound("spare"), false); !errors.Is(err, transaction.ErrStaleGeneration) {
		t.Fatalf("stale Mutate() error = %v", err)
	}
	metadata, err := engine.Mutate(context.Background(), 0, PlanDeleteOutbound("spare"), false)
	if err != nil || metadata.Generation != 1 {
		t.Fatalf("Mutate() = %#v, %v", metadata, err)
	}
	final, _ := os.ReadFile(configPath)
	if bytes.Contains(final, []byte(`"tag": "spare"`)) || !bytes.Contains(final, []byte("// operator note stays")) {
		t.Fatalf("unexpected final config:\n%s", final)
	}
}
