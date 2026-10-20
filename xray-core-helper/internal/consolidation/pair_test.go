package consolidation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/usergeoip"
)

const testGeoipName = "geoip_user.dat"

// Seed the shared table directly: public IPv4 input remains gated until the
// runtime and CLI/web integration is enabled in D3/D6.
func seedMixedDesired(t *testing.T, f *testFixture) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(f.root, "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, value := range []string{"a.example", "192.0.2.9", "203.0.113.7"} {
		if _, err := db.Exec("INSERT INTO proxy_domain_state(domain,desired,baseline) VALUES (?,1,0)", value); err != nil {
			t.Fatal(err)
		}
	}
}

func pairContents(t *testing.T, f *testFixture) [2][]byte {
	t.Helper()
	var out [2][]byte
	for i, name := range []string{defaultTestCandidateName, testGeoipName} {
		data, err := os.ReadFile(filepath.Join(f.assetRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		out[i] = data
	}
	return out
}

func assertEmptyBaseline(t *testing.T, f *testFixture) {
	t.Helper()
	snapshot, err := f.store.Snapshot(context.Background())
	if err != nil || len(snapshot.Baseline) != 0 {
		t.Fatalf("baseline changed: %#v, %v", snapshot, err)
	}
}

func TestPairConsolidationCommitsOneMixedBaseline(t *testing.T) {
	f := newFixture(t)
	seedMixedDesired(t, f)
	ctx := context.Background()
	if err := f.service.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if f.runner.input != "domain:a.example\n" {
		t.Fatalf("geosite input contains IPv4: %q", f.runner.input)
	}
	expected, err := usergeoip.Encode([]string{"192.0.2.9", "203.0.113.7"})
	if err != nil {
		t.Fatal(err)
	}
	if got := pairContents(t, f)[1]; !bytes.Equal(got, expected) {
		t.Fatalf("geoip = %x; want %x", got, expected)
	}
	assertEmptyBaseline(t, f)
	f.assertFrozen(t, true)
	if err := f.service.VerifyWhileLocked(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Finalize(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.store.Snapshot(ctx)
	if err != nil || snapshot.ConsolidationActive || !reflect.DeepEqual(snapshot.Baseline, snapshot.Desired) || len(snapshot.Baseline) != 3 {
		t.Fatalf("shared commit = %#v, %v", snapshot, err)
	}
	if _, err := os.Stat(filepath.Join(f.assetRoot, pendingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging remains: %v", err)
	}
}

func TestPairCrashRecoveryRestoresBothFilesAndMetadata(t *testing.T) {
	for _, existingIP := range []bool{false, true} {
		for _, interruptedMember := range []string{defaultTestCandidateName, testGeoipName} {
			t.Run(interruptedMember+map[bool]string{false: "-new", true: "-existing"}[existingIP], func(t *testing.T) {
				f := newFixture(t)
				seedMixedDesired(t, f)
				old := map[string][]byte{defaultTestCandidateName: []byte("previous geosite")}
				if existingIP {
					old[testGeoipName] = []byte("previous geoip")
				}
				for name, data := range old {
					if err := os.WriteFile(filepath.Join(f.assetRoot, name), data, 0640); err != nil {
						t.Fatal(err)
					}
				}
				ctx := context.Background()
				if err := f.service.Prepare(ctx); err != nil {
					t.Fatal(err)
				}
				// Recreate the durable state of a crash between the two replacements,
				// including an interrupted rollback in the opposite direction.
				path := filepath.Join(f.assetRoot, interruptedMember)
				if data, exists := old[interruptedMember]; exists {
					if err := os.WriteFile(path, data, 0640); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				inspection, err := f.service.Inspect(ctx)
				if err != nil || inspection.Live != LiveAssetsMixed || !inspection.PreparedEvidence {
					t.Fatalf("mixed inspection = %#v, %v", inspection, err)
				}
				if err := f.service.Finalize(ctx); !errors.Is(err, ErrExternalDrift) {
					t.Fatalf("partial pair finalized: %v", err)
				}
				assertEmptyBaseline(t, f)
				// A fresh DB handle and recovery service must use persisted evidence only.
				store, err := domainstore.Open(ctx, filepath.Join(f.root, "helper.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				recovery, err := NewRecoveryService(store, testAssetPath, defaultTestCandidateName, testGeoipName, f.files)
				if err != nil {
					t.Fatal(err)
				}
				if err := recovery.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{defaultTestCandidateName, testGeoipName} {
					data, err := os.ReadFile(filepath.Join(f.assetRoot, name))
					if want, exists := old[name]; exists {
						if err != nil || !bytes.Equal(data, want) {
							t.Fatalf("restored %s = %q, %v", name, data, err)
						}
						info, err := os.Stat(filepath.Join(f.assetRoot, name))
						if err != nil || info.Mode().Perm() != 0640 {
							t.Fatalf("restored metadata %s = %v, %v", name, info, err)
						}
					} else if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("new geoip not removed: %v", err)
					}
				}
				f.assertFrozen(t, false)
				assertEmptyBaseline(t, f)
			})
		}
	}
}

func TestPairSecondPublishFailureRestoresFirst(t *testing.T) {
	for _, event := range []string{"rename:asset/" + testGeoipName, "syncfile:asset/." + testGeoipName + ".xch-candidate"} {
		t.Run(event, func(t *testing.T) {
			f := newFixture(t)
			old := []byte("original geosite")
			if err := os.WriteFile(filepath.Join(f.assetRoot, defaultTestCandidateName), old, 0600); err != nil {
				t.Fatal(err)
			}
			f.files.failOn(event, 1)
			if err := f.service.Prepare(context.Background()); !errors.Is(err, ErrFilesystem) {
				t.Fatalf("injected failure = %v", err)
			}
			got, err := os.ReadFile(filepath.Join(f.assetRoot, defaultTestCandidateName))
			if err != nil || !bytes.Equal(got, old) {
				t.Fatalf("first file not restored: %q, %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(f.assetRoot, testGeoipName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("second file remains: %v", err)
			}
			f.assertFrozen(t, false)
			assertEmptyBaseline(t, f)
		})
	}
}

func TestPairGeoipDriftBlocksAnyRollbackWrite(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.service.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	before := pairContents(t, f)
	if err := os.WriteFile(filepath.Join(f.assetRoot, testGeoipName), []byte("external geoip"), 0644); err != nil {
		t.Fatal(err)
	}
	eventsBefore := len(f.files.eventsCopy())
	if err := f.service.Rollback(ctx); !errors.Is(err, ErrExternalDrift) {
		t.Fatalf("external drift accepted: %v", err)
	}
	after := pairContents(t, f)
	if !bytes.Equal(before[0], after[0]) || string(after[1]) != "external geoip" {
		t.Fatal("rollback changed live files despite drift")
	}
	for _, event := range f.files.eventsCopy()[eventsBefore:] {
		if event == "rename:asset/"+defaultTestCandidateName || event == "remove:asset/"+defaultTestCandidateName {
			t.Fatalf("premature rollback write: %s", event)
		}
	}
	f.assertFrozen(t, true)
	assertEmptyBaseline(t, f)
}

func TestPairManifestAndGeoipEvidenceFailClosed(t *testing.T) {
	for _, mutation := range []string{"missing record", "wrong name", "wrong backup", "candidate changed", "backup changed"} {
		t.Run(mutation, func(t *testing.T) {
			f := newFixture(t)
			if err := os.WriteFile(filepath.Join(f.assetRoot, testGeoipName), []byte("old geoip"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := f.service.Prepare(ctx); err != nil {
				t.Fatal(err)
			}
			before := pairContents(t, f)
			stage := filepath.Join(f.assetRoot, pendingName)
			path := filepath.Join(stage, manifestName)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "missing record":
				delete(manifest, "geoip")
			case "wrong name":
				manifest["geoip"].(map[string]any)["name"] = "geoip_other.dat"
			case "wrong backup":
				manifest["geoip"].(map[string]any)["backupName"] = "../escape"
			case "candidate changed":
				path = filepath.Join(stage, testGeoipName)
			case "backup changed":
				path = filepath.Join(stage, testGeoipName+".previous")
			}
			raw = []byte("changed bytes")
			if mutation != "candidate changed" && mutation != "backup changed" {
				raw, err = json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := f.service.Rollback(ctx); !errors.Is(err, ErrManifest) {
				t.Fatalf("unsafe evidence accepted: %v", err)
			}
			if got := pairContents(t, f); !reflect.DeepEqual(got, before) {
				t.Fatal("malformed evidence changed live pair")
			}
			f.assertFrozen(t, true)
		})
	}
}

func TestPairRollbackRetriesAfterSecondRestoreFailure(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{defaultTestCandidateName, testGeoipName} {
		if err := os.WriteFile(filepath.Join(f.assetRoot, name), []byte("old "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := f.service.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	f.files.failOn("rename:asset/"+testGeoipName, f.files.count("rename:asset/"+testGeoipName)+1)
	if err := f.service.Rollback(ctx); !errors.Is(err, ErrRollback) {
		t.Fatalf("restore failure = %v", err)
	}
	f.assertFrozen(t, true)
	inspection, err := f.service.Inspect(ctx)
	if err != nil || inspection.Live != LiveAssetsMixed {
		t.Fatalf("interrupted rollback = %#v, %v", inspection, err)
	}
	if err := f.service.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{defaultTestCandidateName, testGeoipName} {
		if got := pairContents(t, f)[i]; string(got) != "old "+name {
			t.Fatalf("restored %s = %q", name, got)
		}
	}
	f.assertFrozen(t, false)
	assertEmptyBaseline(t, f)
}

func TestPairBackupFailureDoesNotPublishEitherCandidate(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{defaultTestCandidateName, testGeoipName} {
		if err := os.WriteFile(filepath.Join(f.assetRoot, name), []byte("old "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := pairContents(t, f)
	f.files.failOn("syncfile:stage/"+testGeoipName+".previous", 1)
	if err := f.service.Prepare(context.Background()); !errors.Is(err, ErrFilesystem) {
		t.Fatalf("backup failure = %v", err)
	}
	if after := pairContents(t, f); !reflect.DeepEqual(before, after) {
		t.Fatal("failed backup published a candidate")
	}
	f.assertFrozen(t, false)
}

func TestPairInspectRejectsUnsafeGeoipWithoutManifest(t *testing.T) {
	f := newFixture(t)
	if err := os.Symlink(filepath.Join(f.root, "missing"), filepath.Join(f.assetRoot, testGeoipName)); err != nil {
		t.Fatal(err)
	}
	inspection, err := f.service.Inspect(context.Background())
	if err != nil || inspection.Phase != InspectionInvalid {
		t.Fatalf("unsafe clean geoip = %#v, %v", inspection, err)
	}
}
