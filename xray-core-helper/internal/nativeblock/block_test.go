package nativeblock

import (
	"bytes"
	"errors"
	"testing"
)

func TestBuild(t *testing.T) {
	template := []byte("ignored template header\n" + Begin + "\nnew rules\n" + End + "\nignored template footer\n")
	block := Begin + "\nnew rules\n" + End + "\n"
	tests := []struct {
		name, system, want              string
		initialize, initialized, review bool
		err                             error
	}{
		{name: "replace", system: "header\n" + Begin + "\nold rules\n" + End + "\nfooter", want: "header\n" + block + "footer"},
		{name: "preserve marker spelling", system: "\t" + Begin + "\r\nold\r\n  " + End, want: "\t" + Begin + "\r\nnew rules\n  " + End},
		{name: "empty requires explicit action", err: ErrInitialize},
		{name: "nonempty requires explicit action", system: "existing\n", err: ErrInitialize},
		{name: "initialize empty", initialize: true, initialized: true, want: block},
		{name: "append", system: "existing\n", initialize: true, initialized: true, review: true, want: "existing\n" + block},
		{name: "add separator", system: "existing", initialize: true, initialized: true, review: true, want: "existing\n" + block},
		{name: "partial never initializes", system: Begin + "\nold\n", initialize: true, err: ErrMarkers},
		{name: "reversed", system: End + "\n" + Begin + "\n", err: ErrMarkers},
		{name: "duplicate", system: block + block, err: ErrMarkers},
		{name: "nested", system: Begin + "\n" + block + End, err: ErrMarkers},
		{name: "unknown block", system: "# helper:begin other\n# helper:end other\n", initialize: true, err: ErrMarkers},
		{name: "malformed", system: Begin + " trailing\n" + End, initialize: true, err: ErrMarkers},
		{name: "quoted text not a marker", system: "define text = \"" + Begin + "\"\n", initialize: true, initialized: true, review: true, want: "define text = \"" + Begin + "\"\n" + block},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(tt.system)
			priorTemplate := bytes.Clone(template)
			result, err := Build(input, template, tt.initialize)
			if !errors.Is(err, tt.err) {
				t.Fatalf("error = %v, want %v", err, tt.err)
			}
			if err == nil && (string(result.Content) != tt.want || result.Initialized != tt.initialized || result.ReviewRequired != tt.review) {
				t.Fatalf("unexpected candidate or review flags")
			}
			if string(input) != tt.system || !bytes.Equal(template, priorTemplate) {
				t.Fatal("input mutated")
			}
			if err != nil && result.Content != nil {
				t.Fatal("failed build returned candidate")
			}
		})
	}
}

func TestTemplateMarkersRequired(t *testing.T) {
	for _, template := range []string{"", "rules\n", Begin + "\n", End + "\n", Begin + "\n" + Begin + "\n" + End} {
		if _, err := Build(nil, []byte(template), true); !errors.Is(err, ErrMarkers) {
			t.Fatalf("unmarked/malformed template accepted")
		}
	}
}

func TestBounds(t *testing.T) {
	block := []byte(Begin + "\nrules\n" + End + "\n")
	for _, test := range []struct{ system, template []byte }{
		{bytes.Repeat([]byte("x"), MaxBytes+1), block},
		{nil, bytes.Repeat([]byte("x"), MaxBytes+1)},
		{bytes.Repeat([]byte("x"), MaxBytes), block},
	} {
		if _, err := Build(test.system, test.template, true); !errors.Is(err, ErrSize) {
			t.Fatal("oversized candidate accepted")
		}
	}
}

func TestRepeatAndIndependentResult(t *testing.T) {
	template := []byte(Begin + "\nnative content\n" + End + "\n")
	first, err := Build([]byte("existing\n"), template, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(first.Content, template, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Content, second.Content) || second.Initialized || second.ReviewRequired {
		t.Fatal("replacement is not textually idempotent")
	}
	second.Content[0] = '!'
	if first.Content[0] != 'e' {
		t.Fatal("result aliases source")
	}
}
