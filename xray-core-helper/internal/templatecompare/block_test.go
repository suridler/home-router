package templatecompare

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/nativeblock"
	"home-router/xray-core-helper/internal/templatecatalog"
)

func TestNftCompareScopesContentButRevisionIncludesOutside(t *testing.T) {
	dir := t.TempDir()
	entry := templatecatalog.Entry{ID: templatecatalog.Nftables, WorkingPath: filepath.Join(dir, "template"), SystemPath: filepath.Join(dir, "system")}
	block := nativeblock.Begin + "\ntable inet arbitrary {}\n" + nativeblock.End + "\n"
	os.WriteFile(entry.WorkingPath, []byte("# template\n"+block), 0600)
	os.WriteFile(entry.SystemPath, []byte(block+"# unrelated\n"), 0600)
	c, _ := New(nil)
	first := c.Compare(context.Background(), entry)
	if first.State != api.TemplateSynchronized || first.TemplateHash != first.SystemHash {
		t.Fatal(first)
	}
	os.WriteFile(entry.SystemPath, []byte(block+"# changed unrelated\n"), 0600)
	next := c.Compare(context.Background(), entry)
	if next.State != api.TemplateSynchronized || next.Revision == first.Revision {
		t.Fatal("outside drift hidden from review revision")
	}
	os.WriteFile(entry.SystemPath, []byte("# no markers\n"), 0600)
	unmarked := c.Compare(context.Background(), entry)
	if unmarked.State != api.TemplateDifferent || !unmarked.Summary.BlockInitializationRequired {
		t.Fatal(unmarked)
	}
	os.WriteFile(entry.SystemPath, []byte(nativeblock.Begin+"\n"), 0600)
	invalid := c.Compare(context.Background(), entry)
	if invalid.State != api.TemplateInvalid || invalid.Summary.Code != string(CodeBlock) {
		t.Fatal(invalid)
	}
}
