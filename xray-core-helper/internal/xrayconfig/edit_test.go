package xrayconfig

import (
	"regexp"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/transaction"
)

const editFixture = `{
  "outbounds": [
    // first note
    { "tag": "a", "unknown": 1 },
    { "tag": "b" },
    { "tag": "c" }
  ],
  "empty": [],
  "settings": { "keep": true }
}`

var trailingComma = regexp.MustCompile(`,\s*[\]}]`)

func applyEditsT(t *testing.T, source string, edits ...transaction.ByteEdit) string {
	t.Helper()
	updated, err := transaction.ApplyEdits([]byte(source), edits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseSource(updated); err != nil {
		t.Fatalf("edited document no longer parses: %v\n%s", err, updated)
	}
	return string(updated)
}

func newEditorT(t *testing.T, source string) *editor {
	t.Helper()
	e, err := newEditor([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestAppendArrayItemKeepsCommentsAndUnknownFields(t *testing.T) {
	e := newEditorT(t, editFixture)
	edit, err := e.appendArrayItem(e.syntax.spans.object["outbounds"], map[string]any{"tag": "d", "protocol": "vless"})
	if err != nil {
		t.Fatal(err)
	}
	updated := applyEditsT(t, editFixture, edit)
	for _, kept := range []string{"// first note", `"unknown": 1`, `"settings": { "keep": true }`, `"tag": "d"`} {
		if !strings.Contains(updated, kept) {
			t.Errorf("missing %q:\n%s", kept, updated)
		}
	}
	if trailingComma.MatchString(updated) {
		t.Errorf("trailing comma introduced:\n%s", updated)
	}
}

func TestAppendArrayItemToEmptyArray(t *testing.T) {
	e := newEditorT(t, editFixture)
	edit, err := e.appendArrayItem(e.syntax.spans.object["empty"], "x")
	if err != nil {
		t.Fatal(err)
	}
	updated := applyEditsT(t, editFixture, edit)
	if !strings.Contains(updated, `"empty": [`+"\n"+`    "x"`+"\n"+`  ]`) {
		t.Fatalf("unexpected empty-array insertion:\n%s", updated)
	}
}

func TestDeleteArrayItemCases(t *testing.T) {
	tests := []struct {
		name  string
		index int
		gone  string
		kept  []string
	}{
		{name: "first", index: 0, gone: `"tag": "a"`, kept: []string{`"tag": "b"`, `"tag": "c"`}},
		{name: "middle", index: 1, gone: `"tag": "b"`, kept: []string{`"tag": "a"`, `"tag": "c"`, "// first note"}},
		{name: "last", index: 2, gone: `"tag": "c"`, kept: []string{`"tag": "a"`, `"tag": "b"`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := newEditorT(t, editFixture)
			edits, err := e.deleteArrayItem(e.syntax.spans.object["outbounds"], test.index)
			if err != nil {
				t.Fatal(err)
			}
			updated := applyEditsT(t, editFixture, edits...)
			if strings.Contains(updated, test.gone) {
				t.Errorf("%q still present:\n%s", test.gone, updated)
			}
			for _, kept := range append(test.kept, `"settings": { "keep": true }`) {
				if !strings.Contains(updated, kept) {
					t.Errorf("missing %q:\n%s", kept, updated)
				}
			}
			if trailingComma.MatchString(updated) {
				t.Errorf("trailing comma left behind:\n%s", updated)
			}
		})
	}
}

func TestDeleteOnlyArrayItem(t *testing.T) {
	source := "{\n  \"list\": [\n    1\n  ]\n}"
	e := newEditorT(t, source)
	edits, err := e.deleteArrayItem(e.syntax.spans.object["list"], 0)
	if err != nil {
		t.Fatal(err)
	}
	updated := applyEditsT(t, source, edits...)
	if strings.Contains(updated, "1") {
		t.Fatalf("item not removed:\n%s", updated)
	}
}

func TestDeleteArrayItemRejectsBadIndex(t *testing.T) {
	e := newEditorT(t, editFixture)
	for _, index := range []int{-1, 3} {
		if _, err := e.deleteArrayItem(e.syntax.spans.object["outbounds"], index); err == nil {
			t.Errorf("index %d accepted", index)
		}
	}
}

func TestAppendObjectMemberAndReplaceNode(t *testing.T) {
	e := newEditorT(t, editFixture)
	member, err := e.appendObjectMember(e.syntax.spans.object["settings"], "added", map[string]any{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	replace, err := e.replaceNode(e.syntax.spans.object["outbounds"].array[1].object["tag"], "renamed")
	if err != nil {
		t.Fatal(err)
	}
	updated := applyEditsT(t, editFixture, member, replace)
	for _, want := range []string{`"keep": true`, `"added": {`, `"tag": "renamed"`, "// first note"} {
		if !strings.Contains(updated, want) {
			t.Errorf("missing %q:\n%s", want, updated)
		}
	}
}

func TestRenderJSONDoesNotEscapeHTML(t *testing.T) {
	rendered, err := renderJSON("a&b<c>", "")
	if err != nil || string(rendered) != `"a&b<c>"` {
		t.Fatalf("rendered = %s, error = %v", rendered, err)
	}
}

const memberFixture = `{
  "rule": {
    // description kept
    "ruleTag": "r",
    "domain": ["a"],
    "ip": ["b"],
    "outboundTag": "x",
    "custom": { "keep": 1 }
  },
  "list": [
    { "n": 1 },
    { "n": 2 }
  ]
}`

func TestDeleteMembersCombinations(t *testing.T) {
	tests := []struct {
		name   string
		remove []string
		gone   []string
		kept   []string
	}{
		{"middle run", []string{"domain", "ip"}, []string{`"domain"`, `"ip"`}, []string{`"ruleTag"`, `"outboundTag"`, `"custom"`, "// description kept"}},
		{"tail run", []string{"outboundTag", "custom"}, []string{`"outboundTag"`, `"custom"`}, []string{`"ruleTag"`, `"domain"`, `"ip"`, "// description kept"}},
		{"head run", []string{"ruleTag", "domain"}, []string{`"ruleTag"`, `"domain"`}, []string{`"ip"`, `"custom"`}},
		{"split runs", []string{"domain", "outboundTag"}, []string{`"domain"`, `"outboundTag"`}, []string{`"ruleTag"`, `"ip"`, `"custom"`}},
		{"everything", []string{"ruleTag", "domain", "ip", "outboundTag", "custom"}, []string{`"ruleTag"`, `"custom"`}, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := newEditorT(t, memberFixture)
			names := map[string]bool{}
			for _, name := range test.remove {
				names[name] = true
			}
			updated := applyEditsT(t, memberFixture, e.deleteMembers(e.syntax.spans.object["rule"], names)...)
			for _, gone := range test.gone {
				if strings.Contains(updated, gone) {
					t.Errorf("%s still present:\n%s", gone, updated)
				}
			}
			for _, kept := range append(test.kept, `"list": [`) {
				if !strings.Contains(updated, kept) {
					t.Errorf("missing %s:\n%s", kept, updated)
				}
			}
			if trailingComma.MatchString(updated) {
				t.Errorf("trailing comma left behind:\n%s", updated)
			}
		})
	}
}

func TestInsertArrayItemAt(t *testing.T) {
	for _, index := range []int{0, 1, 2} {
		e := newEditorT(t, memberFixture)
		edit, err := e.insertArrayItemAt(e.syntax.spans.object["list"], index, map[string]any{"n": 9})
		if err != nil {
			t.Fatal(err)
		}
		updated := applyEditsT(t, memberFixture, edit)
		parsed, err := parseSource([]byte(updated))
		if err != nil {
			t.Fatal(err)
		}
		list := asSlice(parsed.value["list"])
		if len(list) != 3 || asMap(list[index])["n"].(interface{ String() string }).String() != "9" {
			t.Errorf("index %d: list = %#v\n%s", index, list, updated)
		}
	}
	e := newEditorT(t, memberFixture)
	if _, err := e.insertArrayItemAt(e.syntax.spans.object["list"], 3, 1); err == nil {
		t.Fatal("out-of-range position accepted")
	}
}
