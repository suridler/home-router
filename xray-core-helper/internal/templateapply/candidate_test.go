package templateapply

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/nativeblock"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatecompare"
)

func TestMergedCandidatePreservesOutsideAndDetectsOutsideDrift(t *testing.T) {
	root := t.TempDir()
	entry := templatecatalog.Entry{ID: templatecatalog.Nftables, WorkingPath: filepath.Join(root, "template"), SystemPath: filepath.Join(root, "system")}
	block := func(body string) string { return nativeblock.Begin + "\n" + body + "\n" + nativeblock.End + "\n" }
	os.WriteFile(entry.WorkingPath, []byte("# template header\n"+block("table ip replacement {}")), 0600)
	os.WriteFile(entry.SystemPath, []byte("# existing before\n"+block("table ip old {}")+"# existing after\n"), 0600)
	c, err := mergeCandidate(entry, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	raw, err := os.ReadFile(c.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "# existing before\n"+block("table ip replacement {}")+"# existing after\n" {
		t.Fatal("outside bytes not preserved")
	}
	os.WriteFile(entry.SystemPath, append(raw, []byte("# external edit\n")...), 0600)
	other, err := mergeCandidate(entry, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.close()
	if c.hash == other.hash {
		t.Fatal("outside edit did not invalidate candidate")
	}
}

func TestInitializationRequiresExplicitCurrentRevision(t *testing.T) {
	root := t.TempDir()
	entry := templatecatalog.Entry{ID: templatecatalog.Nftables, WorkingPath: filepath.Join(root, "template"), SystemPath: filepath.Join(root, "system")}
	os.WriteFile(entry.WorkingPath, []byte(nativeblock.Begin+"\ntable ip profile {}\n"+nativeblock.End+"\n"), 0600)
	os.WriteFile(entry.SystemPath, []byte("# retain without newline"), 0600)
	e := &Engine{hash: nativeHash}
	if _, err := e.candidate(context.Background(), entry, true); !IsCode(err, CodeInitialize) {
		t.Fatal("implicit initialization", err)
	}
	if _, err := e.candidate(WithBlockInitialization(context.Background(), strings.Repeat("a", 64)), entry, true); !IsCode(err, CodeGeneration) {
		t.Fatal("stale review accepted", err)
	}
	comparer, _ := templatecompare.New(nil)
	status := comparer.Compare(context.Background(), entry)
	c, err := e.candidate(WithBlockInitialization(context.Background(), status.Revision), entry, true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	raw, _ := os.ReadFile(c.path)
	if !strings.HasPrefix(string(raw), "# retain without newline\n"+nativeblock.Begin) {
		t.Fatal("prior file changed")
	}
}
