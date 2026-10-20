package nativeblock

import (
	"bytes"
	"testing"
)

func FuzzBuild(f *testing.F) {
	f.Add([]byte("existing\n"), []byte(Begin+"\nnative\n"+End+"\n"), true)
	f.Add([]byte(Begin+"\nold\n"+End), []byte(Begin+"\nnew\n"+End), false)
	f.Fuzz(func(t *testing.T, system, template []byte, initialize bool) {
		if len(system) > MaxBytes || len(template) > MaxBytes {
			t.Skip()
		}
		priorSystem, priorTemplate := bytes.Clone(system), bytes.Clone(template)
		result, err := Build(system, template, initialize)
		if !bytes.Equal(system, priorSystem) || !bytes.Equal(template, priorTemplate) {
			t.Fatal("input changed")
		}
		if err != nil {
			return
		}
		repeated, err := Build(result.Content, template, false)
		if err != nil || !bytes.Equal(result.Content, repeated.Content) {
			t.Fatal("result cannot be reapplied identically")
		}
	})
}
