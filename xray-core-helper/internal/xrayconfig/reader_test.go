package xrayconfig

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONCProjectionsPreserveOrderAndRedactSecrets(t *testing.T) {
	fixture, err := os.ReadFile("testdata/projections.jsonc")
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(Settings{Config: configPath, AssetDir: t.TempDir()})

	summary, err := reader.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(summary.Sections, ",") != "log,routing,inbounds,outbounds,geodata" {
		t.Fatalf("sections = %#v", summary.Sections)
	}
	if summary.Generation != 0 || len(summary.ContentHash) != 64 || summary.ModifiedAt.IsZero() {
		t.Fatalf("metadata = %#v", summary.ConfigMetadata)
	}

	inbounds, err := reader.Inbounds(context.Background())
	if err != nil || len(inbounds.Inbounds) != 1 || inbounds.Inbounds[0].Port != json.Number("52345") {
		t.Fatalf("inbounds = %#v, error = %v", inbounds, err)
	}
	outbounds, err := reader.Outbounds(context.Background())
	if err != nil || len(outbounds.Outbounds) != 2 || outbounds.Outbounds[0].Protocol != "vless" {
		t.Fatalf("outbounds = %#v, error = %v", outbounds, err)
	}

	routing, err := reader.Routing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routing.Rules) != 2 || routing.Rules[0].Index != 1 || routing.Rules[1].Index != 2 {
		t.Fatalf("rule order = %#v", routing.Rules)
	}
	if !routing.Rules[0].Protected || routing.Rules[0].ProtectionReason != "TProxy gateway rule" {
		t.Fatalf("TProxy rule protection = %#v", routing.Rules[0])
	}
	if !routing.Rules[1].Protected || routing.Rules[1].ProtectionReason != "DNS gateway rule" {
		t.Fatalf("DNS rule protection = %#v", routing.Rules[1])
	}
	if !strings.Contains(routing.Rules[0].Description, "[redacted URI]") ||
		!strings.Contains(routing.Rules[0].Description, "[redacted UUID]") ||
		!strings.Contains(routing.Rules[0].Description, "shortId: [redacted]") ||
		!strings.Contains(routing.Rules[0].Description, "accessToken: [redacted]") {
		t.Fatalf("description redaction = %q", routing.Rules[0].Description)
	}
	if len(routing.Balancers) != 1 || routing.Balancers[0].Tag != "auto" ||
		strings.Join(routing.Balancers[0].Selector, ",") != "proxy,backup" || routing.Balancers[0].Strategy != "roundRobin" {
		t.Fatalf("balancers = %#v", routing.Balancers)
	}

	logging, err := reader.Logging(context.Background())
	if err != nil || logging.Loglevel != "debug" || !logging.DNSLog {
		t.Fatalf("logging = %#v, error = %v", logging, err)
	}
	geodata, err := reader.Geodata(context.Background())
	if err != nil || len(geodata.Files) != 2 || geodata.Files[0].Present || geodata.Files[1].Present {
		t.Fatalf("geodata = %#v, error = %v", geodata, err)
	}

	serialized, err := json.Marshal([]any{summary, inbounds, outbounds, routing, logging, geodata})
	if err != nil {
		t.Fatal(err)
	}
	for _, secretValue := range []string{
		"outbound-secret", "private-secret", "short-secret", "rule-secret", "geodata-secret", "token-secret", "top-secret", "bearer-secret",
		"11111111-1111-4111-8111-111111111111", "77777777-7777-7777-7777-777777777777", "vless://user:secret@",
	} {
		if strings.Contains(string(serialized), secretValue) {
			t.Errorf("projection leaked %q: %s", secretValue, serialized)
		}
	}
	for _, unknown := range []string{"unknownInbound", "unknownRule", "unknownTopLevel", "not returned"} {
		if strings.Contains(string(serialized), unknown) {
			t.Errorf("projection exposed unknown config field %q", unknown)
		}
	}
}

func TestMissingSectionsProduceEmptyTypedViews(t *testing.T) {
	reader := readerForContent(t, `{"unknown":{"password":"hidden"}}`)
	inbounds, err := reader.Inbounds(context.Background())
	if err != nil || len(inbounds.Inbounds) != 0 {
		t.Fatalf("inbounds = %#v, error = %v", inbounds, err)
	}
	outbounds, err := reader.Outbounds(context.Background())
	if err != nil || len(outbounds.Outbounds) != 0 {
		t.Fatalf("outbounds = %#v, error = %v", outbounds, err)
	}
	routing, err := reader.Routing(context.Background())
	if err != nil || len(routing.Rules) != 0 || len(routing.Balancers) != 0 {
		t.Fatalf("routing = %#v, error = %v", routing, err)
	}
	logging, err := reader.Logging(context.Background())
	if err != nil || logging.Loglevel != "warning" || logging.Access != "" {
		t.Fatalf("logging = %#v, error = %v", logging, err)
	}
}

func TestMalformedJSONCReturnsSafeError(t *testing.T) {
	reader := readerForContent(t, `{"password":"must-not-appear", /* unterminated`)
	_, err := reader.Summary(context.Background())
	if err == nil || strings.Contains(err.Error(), "must-not-appear") {
		t.Fatalf("Summary() error = %v; want safe parse error", err)
	}
}

func TestStatusReportsPersistedGenerationAndExternalDrift(t *testing.T) {
	reader := readerForContent(t, `{"inbounds":[],"outbounds":[]}`)
	reader.metadata = fixedMetadataSource{generation: 12, contentHash: "previous-hash"}
	status := reader.Status(context.Background())
	if status.ConfigState != "drift" || !status.ConfigReadable || !status.ConfigParsed || !status.ConfigDrift {
		t.Fatalf("status = %#v; want parsed external drift", status)
	}
	if status.Generation != 12 || status.ContentHash == "previous-hash" || status.ContentHash == "" {
		t.Fatalf("status metadata = %#v", status.ConfigMetadata)
	}
}

func readerForContent(t *testing.T, content string) Reader {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return NewReader(Settings{Config: configPath, AssetDir: t.TempDir()})
}

type fixedMetadataSource struct {
	generation  uint64
	contentHash string
}

func (source fixedMetadataSource) ConfigMetadata(context.Context) (uint64, string, error) {
	return source.generation, source.contentHash, nil
}
