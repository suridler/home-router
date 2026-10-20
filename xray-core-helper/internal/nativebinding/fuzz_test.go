package nativebinding

import (
	"bytes"
	"testing"
)

func FuzzParse(f *testing.F) {
	f.Add([]byte("unannotated native content\n"))
	f.Add([]byte(marked("mark", "uint32", "xray.intercept-mark", "0x1", "native 0x1")))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		before := bytes.Clone(raw)
		doc, err := Parse(raw)
		if !bytes.Equal(before, raw) {
			t.Fatal("parser changed input")
		}
		if err != nil {
			return
		}
		if len(doc.Bindings()) == 0 {
			out, err := doc.Render(nil)
			if err != nil || !bytes.Equal(out, raw) {
				t.Fatal("unannotated content changed")
			}
		}
	})
}
