package setup

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/filemetadata"
	"home-router/xray-core-helper/internal/templatecatalog"
	"os"
	"syscall"
	"testing"
)

func TestDestinationMetadataIsSeparateFromPrivateCandidatesAndRollback(t *testing.T) {
	s, entries, _ := fileStoreFixture(t)
	s.metadata = func(id templatecatalog.ID) (filemetadata.Metadata, error) {
		mode := os.FileMode(0644)
		if id == templatecatalog.XrayConfig {
			mode = 0640
		}
		return filemetadata.Metadata{Mode: mode, UID: os.Getuid(), GID: os.Getgid()}, nil
	}
	tx, err := s.Prepare(context.Background(), Plan{Interface: "lan0", LANNetwork: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, item := range tx.(*fileTransaction).manifest.Items {
		info, err := os.Stat(item.Target)
		if err != nil || uint32(info.Mode().Perm()) != item.ApplyMode {
			t.Fatalf("metadata %s: %v", item.ID, err)
		}
		private, _ := os.Stat(item.Candidate)
		if private.Mode().Perm() != 0600 {
			t.Fatal("candidate exposed")
		}
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(entries[0].SystemPath)
	if info.Mode().Perm() != 0640 {
		t.Fatal("original permissions lost")
	}
}
func TestMissingDestinationIdentityFailsBeforePublishing(t *testing.T) {
	s, entries, _ := fileStoreFixture(t)
	s.metadata = func(templatecatalog.ID) (filemetadata.Metadata, error) {
		return filemetadata.Metadata{}, errors.New("missing identity")
	}
	if _, err := s.Prepare(context.Background(), Plan{}); err == nil {
		t.Fatal("prepare succeeded")
	}
	if _, err := os.Stat(s.pendingPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("manifest published")
	}
	_ = s.validate(context.Background(), entries[0].ID, entries[0].WorkingPath)
}

// Gateway preparation preserves all working files, including Xray and metadata.
func TestGatewayWorkingMetadataMatchesBootPolicy(t *testing.T) {
	s, entries, _ := fileStoreFixture(t)
	s.workingMetadata = filemetadata.Working
	raw, err := os.ReadFile("../../templConf/xray/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(entries[0].WorkingPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	plan := Plan{Interface: "lan0", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24"}
	candidate, gateway, err := s.gatewayCandidate(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Gateway = &gateway
	plan.XrayCandidateHash = contentHash(candidate)
	tx, err := s.Prepare(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	working := 0
	for _, item := range tx.(*fileTransaction).manifest.Items {
		for _, entry := range entries {
			if item.Target != entry.WorkingPath {
				continue
			}
			working++
			info, err := os.Stat(item.Target)
			if err != nil {
				t.Fatal(err)
			}
			// Rollback metadata remains the original metadata, separately from apply.
			st := info.Sys().(*syscall.Stat_t)
			if !item.Preserve || item.ApplyMode != uint32(info.Mode().Perm()) || item.ApplyUID != int(st.Uid) || item.ApplyGID != int(st.Gid) {
				t.Fatal("working template would be modified")
			}
			if item.UID != int(st.Uid) || item.GID != int(st.Gid) || item.Mode != uint32(info.Mode().Perm()) {
				t.Fatal("rollback metadata changed")
			}
		}
	}
	if working != len(entries) {
		t.Fatalf("working templates: %d", working)
	}
	if err := tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
}
