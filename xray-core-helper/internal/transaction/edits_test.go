package transaction

import (
	"bytes"
	"testing"
)

func TestApplyEditsPreservesCommentsAndUnknownFields(t *testing.T) {
	source := []byte(`{
  // keep this operator note
  "log": { "loglevel": "warning", "unknownLogOption": true },
  "routing": { "unknownRoutingOption": { "retain": 7 } }
}`)
	start := bytes.Index(source, []byte(`"warning"`))
	if start < 0 {
		t.Fatal("fixture loglevel not found")
	}
	updated, err := ApplyEdits(source, []ByteEdit{{
		Start:       start,
		End:         start + len(`"warning"`),
		Replacement: []byte(`"debug"`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, retained := range []string{
		"// keep this operator note", `"unknownLogOption": true`, `"unknownRoutingOption": { "retain": 7 }`,
	} {
		if !bytes.Contains(updated, []byte(retained)) {
			t.Errorf("edit removed preserved JSONC content %q: %s", retained, updated)
		}
	}
	if !bytes.Contains(updated, []byte(`"loglevel": "debug"`)) {
		t.Fatalf("replacement not applied: %s", updated)
	}
}

func TestApplyEditsRejectsInvalidAndOverlappingRanges(t *testing.T) {
	tests := []struct {
		name  string
		edits []ByteEdit
	}{
		{name: "negative offset", edits: []ByteEdit{{Start: -1, End: 0}}},
		{name: "end past input", edits: []ByteEdit{{Start: 0, End: 4}}},
		{name: "reversed range", edits: []ByteEdit{{Start: 2, End: 1}}},
		{name: "overlap", edits: []ByteEdit{{Start: 0, End: 2}, {Start: 1, End: 3}}},
		{name: "duplicate insertion point", edits: []ByteEdit{{Start: 1, End: 1}, {Start: 1, End: 1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ApplyEdits([]byte("abc"), test.edits); err == nil {
				t.Fatal("ApplyEdits() succeeded; want range error")
			}
		})
	}
}
