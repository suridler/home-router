package templateapply

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/templatecatalog"
)

func groupEngineFixture(t *testing.T) *Engine {
	t.Helper()
	e, _ := engineTestFixture(t, runtimeStub{}, replaceStub{})
	e.groupPaths = map[templatecatalog.ID]string{}
	for _, id := range []templatecatalog.ID{templatecatalog.Nftables, templatecatalog.Sysctl, templatecatalog.TProxyUnit} {
		e.groupPaths[id] = filepath.Join(t.TempDir(), string(id))
	}
	return e
}

func TestGroupFilesRestoreBytesMetadataAndAbsence(t *testing.T) {
	e := groupEngineFixture(t)
	ctx := context.Background()
	if err := os.WriteFile(e.groupPaths[templatecatalog.Nftables], []byte("original\n"), 0640); err != nil {
		t.Fatal(err)
	}
	files, err := e.CaptureSystemBaseline(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range e.groupPaths {
		if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if e.VerifySystemBaseline(ctx, files) == nil {
		t.Fatal("mismatch accepted")
	}
	for _, file := range files {
		if err := e.RestoreSystemFile(ctx, file); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.VerifySystemBaseline(ctx, files); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if err := e.RestoreSystemFile(ctx, file); err != nil {
			t.Fatal("retry not idempotent", err)
		}
	}
}

func TestGroupRejectsSymlinksUnknownIDsAndOversize(t *testing.T) {
	e := groupEngineFixture(t)
	ctx := context.Background()
	path := e.groupPaths[templatecatalog.Nftables]
	if err := os.Symlink("missing", path); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CaptureSystemBaseline(ctx); err == nil {
		t.Fatal("symlink captured")
	}
	if err := e.RestoreSystemFile(ctx, BaselineFile{TemplateID: templatecatalog.Nftables}); err == nil {
		t.Fatal("symlink deleted")
	}
	if err := e.RestoreSystemFile(ctx, BaselineFile{TemplateID: templatecatalog.XrayConfig}); err == nil {
		t.Fatal("Xray escaped group boundary")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, (1<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CaptureSystemBaseline(ctx); err == nil {
		t.Fatal("oversize captured")
	}
}

func TestSystemPrimitiveCannotBypassGroupJournal(t *testing.T) {
	e, calls := engineTestFixture(t, runtimeStub{}, replaceStub{})
	for _, id := range []templatecatalog.ID{templatecatalog.Nftables, templatecatalog.Sysctl, templatecatalog.TProxyUnit} {
		if _, err := e.Apply(context.Background(), id, 2); !IsCode(err, CodeSafeApply) {
			t.Fatal(err)
		}
	}
	if len(*calls) != 0 {
		t.Fatal("unsafe primitive ran effects", *calls)
	}
}
