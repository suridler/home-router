package xrayconfig

import (
	"bytes"
	"context"
	"encoding/json"
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

const nativeDraft = `{"protocol":"vless","settings":{"address":"new.example","port":443,"id":"22222222-2222-4222-8222-222222222222","encryption":"none"},"streamSettings":{"network":"tcp"}}`

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

func TestNativeOutboundCreatePreservesOtherObjects(t *testing.T) {
	updated, err := plan(t, PlanWriteOutbound("edge-2", json.RawMessage(nativeDraft), "create"), outboundFixture)
	if err != nil {
		t.Fatal(err)
	}
	parsed := newEditorT(t, updated).syntax
	index, added := findOutbound(parsed, "edge-2")
	if index != 6 {
		t.Fatalf("index=%d", index)
	}
	mark := asMap(asMap(added["streamSettings"])["sockopt"])["mark"]
	if n, ok := markInteger(mark); !ok || n != 255 {
		t.Fatal("mark not inherited")
	}
	for _, kept := range []string{"// operator note stays", `"unknownOutbound": 1`, `"customKeep": true`} {
		if !strings.Contains(updated, kept) {
			t.Fatalf("lost %s", kept)
		}
	}
}
func TestNativeOutboundOperationBoundaries(t *testing.T) {
	for _, tc := range []struct{ tag, raw, op, code string }{
		{"extra", nativeDraft, "create", "outbound_exists"},
		{"block", nativeDraft, "create", "outbound_protected"},
		{"bad tag!", nativeDraft, "create", "invalid_outbound_tag"},
		{"proxy", `{"protocol":"freedom"}`, "edit", "outbound_protocol_changed"},
		{"proxy", `{"tag":"renamed","protocol":"vless"}`, "edit", "outbound_identity_changed"},
		{"direct", nativeDraft, "replace", "outbound_protected"},
		{"ghost", nativeDraft, "edit", "outbound_not_found"},
		{"new", `null`, "create", "invalid_outbound"},
	} {
		t.Run(tc.tag+tc.op+tc.code, func(t *testing.T) {
			_, err := plan(t, PlanWriteOutbound(tc.tag, json.RawMessage(tc.raw), tc.op), outboundFixture)
			if rejectCode(err) != tc.code {
				t.Fatalf("got %v want %s", err, tc.code)
			}
		})
	}
}
func TestNativeEditPreservesUnknownFieldsAndNestedComments(t *testing.T) {
	source := strings.Replace(outboundFixture, `"address": "old.example"`, `/* server note */ "address": "old.example"`, 1)
	parsed := newEditorT(t, source).syntax
	_, current := findOutbound(parsed, "proxy")
	asMap(asSlice(asMap(current["settings"])["vnext"])[0])["address"] = "changed.example"
	raw, _ := json.Marshal(current)
	updated, err := plan(t, PlanWriteOutbound("proxy", raw, "edit"), source)
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Replace(source, `"old.example"`, `"changed.example"`, 1)
	if updated != expected {
		t.Fatalf("edit changed unrelated bytes: %s", updated)
	}
	unchanged, err := plan(t, PlanWriteOutbound("proxy", raw, "edit"), updated)
	if err != nil || unchanged != updated {
		t.Fatal("no-op rewrote native JSONC")
	}
}
func TestReplacementRetainsOnlyTagAndRequiredMark(t *testing.T) {
	source := strings.Replace(outboundFixture, `"tag": "proxy",`, `"tag": "proxy", "sendThrough":"192.0.2.1", "mux":{"enabled":true},`, 1)
	updated, err := plan(t, PlanWriteOutbound("proxy", json.RawMessage(`{"protocol":"trojan","settings":{"address":"new.example","port":443,"password":"test"}}`), "replace"), source)
	if err != nil {
		t.Fatal(err)
	}
	_, current := findOutbound(newEditorT(t, updated).syntax, "proxy")
	if current["sendThrough"] != nil || current["mux"] != nil {
		t.Fatal("old options survived replacement")
	}
	stream := asMap(current["streamSettings"])
	if len(stream) != 1 || len(asMap(stream["sockopt"])) != 1 || asMap(stream["sockopt"])["mark"] == nil {
		t.Fatal("old stream survived replacement")
	}
	if current["tag"] != "proxy" || current["protocol"] != "trojan" {
		t.Fatal("wrong replacement identity")
	}
}
func TestOutboundChainPreventsRemoval(t *testing.T) {
	source := strings.Replace(outboundFixture, `"customKeep": true`, `"customKeep": true, "sockopt": {"mark":255,"dialerProxy":"spare"}`, 1)
	// Replace original sockopt instead of creating duplicate JSON keys.
	source = strings.Replace(source, `"sockopt": { "mark": 255 }, `, "", 1)
	_, err := plan(t, PlanDeleteOutbound("spare"), source)
	if rejectCode(err) != "outbound_in_use" {
		t.Fatalf("chain reference ignored: %v", err)
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
	_, err := plan(t, PlanWriteOutbound("edge-2", json.RawMessage(nativeDraft), "create"), `{"routing":{}}`)
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
