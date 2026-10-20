package templateapply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"home-router/xray-core-helper/internal/templatecatalog"
)

func lifecycleFixture(t *testing.T) *Engine {
	t.Helper()
	e := groupEngineFixture(t)
	root := t.TempDir()
	for _, asset := range DefaultLifecycleAssets() {
		asset.Source = filepath.Join(root, "sources", string(asset.ID))
		asset.Target = filepath.Join(root, "dest", string(asset.ID))
		asset.UID, asset.GID = os.Geteuid(), os.Getegid()
		if err := os.MkdirAll(filepath.Dir(asset.Source), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(asset.Source, []byte("native lifecycle "+string(asset.ID)), 0600); err != nil {
			t.Fatal(err)
		}
		e.lifecycleAssets = append(e.lifecycleAssets, asset)
	}
	return e
}

func TestLifecycleFilesIncludedInDurableBaseline(t *testing.T) {
	e := lifecycleFixture(t)
	ctx := context.Background()
	baseline, err := e.CaptureSystemBaseline(ctx)
	if err != nil || len(baseline) != 4 {
		t.Fatalf("capture %d: %v", len(baseline), err)
	}
	prepared, err := e.prepareLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.installLifecycle(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	if err = e.verifyLifecycle(prepared); err != nil {
		t.Fatal(err)
	}
	if err = e.VerifySystemBaseline(ctx, baseline); err == nil {
		t.Fatal("changed auxiliaries accepted")
	}
	for _, file := range baseline {
		if err = e.RestoreSystemFile(ctx, file); err != nil {
			t.Fatal(err)
		}
	}
	if err = e.VerifySystemBaseline(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	// Historical three-file journals do not invent auxiliary absence.
	if err = e.VerifySystemBaseline(ctx, baseline[:3]); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleRejectsDriftBeforeAnyWrite(t *testing.T) {
	e := lifecycleFixture(t)
	prepared, err := e.prepareLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	last := e.lifecycleAssets[0].Target
	if err = os.MkdirAll(filepath.Dir(last), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(last, []byte("operator change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = e.installLifecycle(context.Background(), prepared); err == nil {
		t.Fatal("drift overwritten")
	}
	if raw, err := os.ReadFile(last); err != nil || string(raw) != "operator change" {
		t.Fatal("operator edit changed before drift rejection", err, string(raw))
	}
}

func TestLifecycleDigestTracksSourceAndInstalledContent(t *testing.T) {
	e := lifecycleFixture(t)
	p, err := e.prepareLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	old := lifecycleHash("candidate", p)
	if err = os.WriteFile(e.lifecycleAssets[0].Source, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := e.prepareLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	if lifecycleHash("candidate", changed) == old {
		t.Fatal("source change invisible")
	}
	if err = e.installLifecycle(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if err = e.verifyLifecycle(changed); err == nil {
		t.Fatal("installed drift invisible")
	}
	if _, err = e.catalog.Lookup(LifecycleDropinID); err == nil {
		t.Fatal("recovery ID became public")
	}
	if _, err = e.groupPath(templatecatalog.XrayConfig); err == nil {
		t.Fatal("Xray escaped baseline boundary")
	}
}

func TestTProxyApplyBindsAuxiliarySourcesAndConfirmsInstalledGroup(t *testing.T) {
	e := lifecycleFixture(t)
	ctx := context.Background()
	descriptor, err := e.PrepareConfirmed(ctx, templatecatalog.TProxyUnit, 2)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.CandidateHash == "validated" {
		t.Fatal("auxiliary bytes missing from descriptor")
	}
	if err = e.ActivateConfirmed(ctx, descriptor); err != nil {
		t.Fatal(err)
	}
	if err = e.Confirm(ctx, descriptor); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(e.lifecycleAssets[0].Target, []byte("operator edit"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = e.Confirm(ctx, descriptor); err == nil {
		t.Fatal("confirmation accepted altered lifecycle script")
	}
}

func TestTProxyPreparationRejectsDriftAfterGroupedCapture(t *testing.T) {
	e := lifecycleFixture(t)
	ctx := context.Background()
	if _, err := e.CaptureSystemBaseline(ctx); err != nil {
		t.Fatal(err)
	}
	target := e.lifecycleAssets[0].Target
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("operator created after capture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PrepareConfirmed(ctx, templatecatalog.TProxyUnit, 2); err == nil {
		t.Fatal("baseline drift accepted")
	}
}

func TestOldRoutingCleanupFailurePreventsNativeReplacement(t *testing.T) {
	e := lifecycleFixture(t)
	r := &runnerStub{onCall: func(c commandCall) error {
		if reflect.DeepEqual(c.args, []string{"stop", "xray-tproxy-routing.service"}) {
			return errors.New("old cleanup failed")
		}
		return nil
	}}
	e.runtime, _ = newCommandRuntime(r)
	descriptor, err := e.PrepareConfirmed(context.Background(), templatecatalog.TProxyUnit, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.ActivateConfirmed(context.Background(), descriptor); err == nil {
		t.Fatal("cleanup failure ignored")
	}
	for _, asset := range e.lifecycleAssets {
		if _, err = os.Stat(asset.Target); !os.IsNotExist(err) {
			t.Fatal("new lifecycle installed before old routing cleanup", asset.ID, err)
		}
	}
}
