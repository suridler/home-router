package jsonrequest

import (
	"strings"
	"testing"
)

type command struct {
	Value  *string `json:"value"`
	Nested struct {
		Name string `json:"name"`
	} `json:"nested"`
	Items []struct {
		Name string `json:"name"`
	} `json:"items"`
}

func TestDecodeCommandEnvelope(t *testing.T) {
	for _, body := range []string{
		"", `null`, `[]`, `1`, `{"value":"one","value":"two"}`,
		`{"nested":{"name":"one","name":"two"}}`,
		`{"items":[{"name":"one","name":"two"}]}`,
		`{"unknown":1}`, `{"nested":{"unknown":1}}`, `{"value":1}`,
		`{"value":"one"}{}`, `{"nested":`,
	} {
		t.Run(body, func(t *testing.T) {
			var value command
			if err := Decode(strings.NewReader(body), &value); err == nil {
				t.Fatalf("accepted %q", body)
			}
		})
	}
	for _, body := range []string{`{}`, `{"value":null}`, `{"value":"ok","nested":{"name":"test"},"items":[{"name":"one"},{"name":"two"}]}`} {
		var value command
		if err := Decode(strings.NewReader(body), &value); err != nil {
			t.Fatalf("rejected %q: %v", body, err)
		}
	}
}
