package setup

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSetupResolvesGIDWithoutChangingSystemOnFailure(t *testing.T) {
	s, entries, _ := fileStoreFixture(t)
	tx, err := s.Prepare(context.Background(), Plan{Interface: "lan0", LANNetwork: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.identity = func(context.Context) (int, error) { return 0, errors.New("secret") }
	if _, err := s.Prepare(context.Background(), Plan{}); err == nil || err.Error() != "preflight_xray_identity_unavailable" {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(entries[0].SystemPath)
	if len(raw) != 0 {
		t.Fatal("system changed before identity resolved")
	}
}
func TestSetupSubstitutesActualGIDOnlyInCandidate(t *testing.T) {
	s, entries, _ := fileStoreFixture(t)
	s.identity = func(context.Context) (int, error) { return 12345, nil }
	tx, err := s.Prepare(context.Background(), Plan{Interface: "eth99", LANNetwork: "10.20.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(tx.(*fileTransaction).manifest.Items[1].Candidate)
	if !strings.Contains(string(raw), "define xray_gid = 12345") {
		t.Fatal("actual GID missing")
	}
	original, _ := os.ReadFile(entries[1].WorkingPath)
	if strings.Contains(string(original), "12345") {
		t.Fatal("working template overwritten")
	}
}

func TestUnmarkedSystemFilesDoNotResolveXrayIdentity(t *testing.T) {
	s, entries, _ := fileStoreFixture(t)
	// This setup subset contains only unannotated unit/sysctl files. It must
	// not need Xray identity just because the store has an identity provider.
	s.entries = entries[2:]
	s.identity = func(context.Context) (int, error) {
		t.Fatal("undeclared identity provider invoked")
		return 0, errors.New("unavailable")
	}
	tx, err := s.Prepare(context.Background(), Plan{})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
}
