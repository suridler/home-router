package xrayconfig

import (
	"context"
	"testing"
)

func firstRuleDescription(t *testing.T, source string) string {
	t.Helper()
	routing, err := readerForContent(t, source).Routing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routing.Rules) == 0 {
		t.Fatal("fixture has no rules")
	}
	return routing.Rules[0].Description
}

func TestDescriptionUsesFirstTopLevelCommentBlockOnly(t *testing.T) {
	source := `{
    "routing": {
      "rules": [{
        "type": "field",
        // First description line.
        // Second description line.
        "domain": [
          // Nested comment must be ignored.
          "example.com"
        ],
        // Later block must be ignored.
        "outboundTag": "direct"
      }]
    }
  }`
	if got, want := firstRuleDescription(t, source), "First description line. Second description line."; got != want {
		t.Fatalf("description = %q; want %q", got, want)
	}
}

func TestDescriptionCleansBlockComment(t *testing.T) {
	source := `{"routing":{"rules":[{
    /* Private destinations.
     * Keep above the proxy rule. */
    "ip":["geoip:private"],"outboundTag":"direct"
  }]}}`
	if got, want := firstRuleDescription(t, source), "Private destinations. Keep above the proxy rule."; got != want {
		t.Fatalf("description = %q; want %q", got, want)
	}
}

func TestDescriptionRedactsCredentials(t *testing.T) {
	source := `{"routing":{"rules":[{
    // Endpoint vless://secret@example.test and UUID 123e4567-e89b-42d3-a456-426614174000
    // privateKey=very-secret-value
    "domain":["example.com"],"outboundTag":"direct"
  }]}}`
	want := "Endpoint [redacted URI] and UUID [redacted UUID] privateKey: [redacted]"
	if got := firstRuleDescription(t, source); got != want {
		t.Fatalf("description = %q; want %q", got, want)
	}
}

func TestDescriptionIsEmptyWithoutComments(t *testing.T) {
	source := `{"routing":{"rules":[{"domain":["example.com"],"outboundTag":"direct"}]}}`
	if got := firstRuleDescription(t, source); got != "" {
		t.Fatalf("description = %q; want empty", got)
	}
}

func TestDescriptionIsCappedAt1000Bytes(t *testing.T) {
	long := ""
	for len(long) < 1500 {
		long += "word "
	}
	source := "{\"routing\":{\"rules\":[{\n// " + long + "\n\"domain\":[\"example.com\"]}]}}"
	if got := firstRuleDescription(t, source); len(got) == 0 || len(got) > 1000 {
		t.Fatalf("description length = %d; want 1..1000", len(got))
	}
}
