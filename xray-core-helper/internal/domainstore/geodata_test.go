package domainstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPendingBaselinesSurviveRestartAndPreserveNewDesiredValues(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	assets := t.TempDir()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"example.com", "192.0.2.1"} {
		if err := s.AddDesired(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	data := []byte("geosite candidate")
	hash := sha256.Sum256(data)
	if err := s.StageBaseline(ctx, "geosite_user.dat", hex.EncodeToString(hash[:]), "old", []string{"example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "geosite_user.dat"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateBaselines(ctx, "old", assets, map[string]bool{"geosite_user.dat": true}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(ctx)
	if len(before.Baseline) != 0 {
		t.Fatal("activated without new Xray runtime")
	}
	if err := s.DeleteDesired(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDesired(ctx, "later.example"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ActivateBaselines(ctx, "new", assets, map[string]bool{"geosite_user.dat": true}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Baseline, []string{"example.com"}) {
		t.Fatal("wrong loaded baseline", snapshot)
	}
	if !reflect.DeepEqual(snapshot.Desired, []string{"192.0.2.1", "later.example"}) {
		t.Fatal("desired mutation lost", snapshot)
	}
}
