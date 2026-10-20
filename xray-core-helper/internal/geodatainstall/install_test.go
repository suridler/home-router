package geodatainstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"home-router/xray-core-helper/internal/geodatalimits"
)

const testAssetDir = "/usr/local/share/xray"

func TestLargeGeositeInstallInspectAndRollback(t *testing.T) {
	fixture := newFixture(t)
	oldPath := filepath.Join(fixture.assetRoot, geositeName)
	if err := os.WriteFile(oldPath, nil, 0644); err != nil {
		t.Fatal(err)
	}
	const oldSize = 73703302
	const newSize = oldSize + 1
	if err := os.Truncate(oldPath, oldSize); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(fixture.sources.GeositePath, newSize); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
		t.Fatal(err)
	}
	inspection, err := fixture.installer.Inspect(context.Background())
	if err != nil || inspection.Phase != InspectionManifest || inspection.Live != [2]LiveAssetPhase{LiveAssetInstalled, LiveAssetInstalled} {
		t.Fatalf("large asset recovery inspection: %+v, %v", inspection, err)
	}
	if info, err := os.Stat(oldPath); err != nil || info.Size() != newSize {
		t.Fatalf("installed asset: %v, %v", info, err)
	}
	if err := fixture.installer.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(oldPath); err != nil || info.Size() != oldSize {
		t.Fatalf("restored asset: %v, %v", info, err)
	}
}

func TestInstallFirstAssetsAndKeepsDurableRecoveryManifest(t *testing.T) {
	fixture := newFixture(t)
	manifest, err := fixture.installer.Install(context.Background(), fixture.sources)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	for name, source := range map[string]string{
		geositeName: fixture.sources.GeositePath,
		geoipName:   fixture.sources.GeoipPath,
	} {
		got, err := os.ReadFile(filepath.Join(fixture.assetRoot, name))
		want, readErr := os.ReadFile(source)
		if err != nil || readErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s = %d bytes, read errors %v/%v; want source bytes", name, len(got), err, readErr)
		}
		info, err := os.Stat(filepath.Join(fixture.assetRoot, name))
		if err != nil || info.Mode().Perm() != installedMode {
			t.Fatalf("%s mode = %v, error = %v; want %04o", name, info, err, installedMode)
		}
	}
	if manifest.Version != manifestVersion || manifest.ID == "" {
		t.Fatalf("manifest header = %#v", manifest)
	}
	for _, record := range manifest.Assets {
		if record.PreviousExists || record.PreviousHash != "" || record.BackupName != "" ||
			record.PreviousMode != 0 || record.PreviousUID != 0 || record.PreviousGID != 0 {
			t.Errorf("first install recorded a previous asset: %#v", record)
		}
	}
	stored, err := fixture.installer.ReadPendingManifest()
	if err != nil || stored == nil || *stored != manifest {
		t.Fatalf("ReadPendingManifest() = %#v, %v; want %#v", stored, err, manifest)
	}
	entries := readEntries(t, filepath.Join(fixture.assetRoot, pendingDirectoryName))
	if !equalStrings(entries, []string{manifestFileName}) {
		t.Fatalf("pending entries = %#v; want manifest only", entries)
	}
	for _, name := range []string{geositeName, geoipName} {
		info, err := os.Stat(filepath.Join(fixture.assetRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		assertOwner(t, info, os.Geteuid(), os.Getegid())
	}
}

func TestInstallOwnershipDoesNotClaimPreexistingManifest(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
		t.Fatal(err)
	}
	beforeGeosite, err := os.ReadFile(filepath.Join(fixture.assetRoot, geositeName))
	if err != nil {
		t.Fatal(err)
	}
	_, recovery, err := fixture.installer.InstallOwnedWhileLocked(context.Background(), fixture.sources)
	if !errors.Is(err, ErrPendingManifest) || recovery != NoInstallRecovery {
		t.Fatalf("InstallOwnedWhileLocked() = recovery %d, error %v; want refusal without ownership", recovery, err)
	}
	afterGeosite, err := os.ReadFile(filepath.Join(fixture.assetRoot, geositeName))
	if err != nil || !bytes.Equal(afterGeosite, beforeGeosite) {
		t.Fatalf("pre-existing live asset changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.assetRoot, pendingDirectoryName, manifestFileName)); err != nil {
		t.Fatalf("pre-existing manifest was changed: %v", err)
	}
}

func TestRollbackRestoresExactPreviousAssetsAndIsIdempotent(t *testing.T) {
	fixture := newFixture(t)
	writeOldAssets(t, fixture)
	if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
		t.Fatal(err)
	}
	if err := fixture.installer.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	assertOldAssets(t, fixture)
	if entries := readEntries(t, fixture.assetRoot); len(entries) != 2 {
		t.Fatalf("asset directory entries after rollback = %v; want only restored assets", entries)
	}
	if err := fixture.installer.Rollback(context.Background()); err != nil {
		t.Fatalf("idempotent Rollback() error = %v", err)
	}
}

func TestRollbackRestoresAbsenceAndRefusesExternalDrift(t *testing.T) {
	t.Run("absence", func(t *testing.T) {
		fixture := newFixture(t)
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
			t.Fatal(err)
		}
		if err := fixture.installer.Rollback(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertNoLiveAssets(t, fixture)
	})
	t.Run("drift", func(t *testing.T) {
		fixture := newFixture(t)
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.assetRoot, geositeName), []byte("external edit"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := fixture.installer.Rollback(context.Background()); !errors.Is(err, ErrExternalDrift) {
			t.Fatalf("Rollback() error = %v; want external drift", err)
		}
		if _, err := os.Stat(filepath.Join(fixture.assetRoot, pendingDirectoryName, manifestFileName)); err != nil {
			t.Fatalf("manifest evidence was not retained: %v", err)
		}
	})
}

func TestRollbackRefusesPreviousHashWithChangedOwnershipMetadata(t *testing.T) {
	fixture := newFixture(t)
	writeOldAssets(t, fixture)
	if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.assetRoot, geositeName)
	if err := os.WriteFile(path, fixture.old[geositeName], 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := fixture.installer.Rollback(context.Background()); !errors.Is(err, ErrExternalDrift) {
		t.Fatalf("Rollback() error = %v; want metadata drift refusal", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.assetRoot, pendingDirectoryName, manifestFileName)); err != nil {
		t.Fatalf("manifest not retained after metadata drift: %v", err)
	}
}

func TestRollbackFailureRetainsEvidenceAndCanBeRetried(t *testing.T) {
	fixture := newFixture(t)
	writeOldAssets(t, fixture)
	if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
		t.Fatal(err)
	}
	fixture.files.failOn("rename:"+geositeName, 2)
	if err := fixture.installer.Rollback(context.Background()); !errors.Is(err, ErrRestorationFailed) {
		t.Fatalf("Rollback() error = %v; want incomplete restore", err)
	}
	if _, err := fixture.installer.ReadPendingManifest(); err != nil {
		t.Fatalf("pending evidence after failed restore is invalid: %v", err)
	}
	if err := fixture.installer.Rollback(context.Background()); err != nil {
		t.Fatalf("retry Rollback() error = %v", err)
	}
	assertOldAssets(t, fixture)
}

func TestCommitDurablyRemovesBackupsBeforeManifest(t *testing.T) {
	fixture := newFixture(t)
	writeOldAssets(t, fixture)
	if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
		t.Fatal(err)
	}
	fixture.files.resetEvents()
	if err := fixture.installer.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := fixture.files.eventsCopy()
	manifest := indexOf(events, "remove:pending/"+manifestFileName)
	firstDirectorySync := indexOf(events, "sync-dir:pending")
	geositeBackup := indexOf(events, "remove:pending/"+backupName(geositeName))
	geoipBackup := indexOf(events, "remove:pending/"+backupName(geoipName))
	secondDirectorySync := indexWithOffset(events, "sync-dir:pending", firstDirectorySync+1)
	if manifest < 0 || firstDirectorySync <= manifest ||
		geositeBackup <= firstDirectorySync || geoipBackup <= firstDirectorySync ||
		secondDirectorySync <= geositeBackup || secondDirectorySync <= geoipBackup {
		t.Fatalf("commit evidence removal order is unsafe: %v", events)
	}
}

func TestCommitAndRollbackCleanupCrashPointsRetainRequiredEvidence(t *testing.T) {
	for _, operation := range []string{"commit", "rollback"} {
		for _, failure := range []struct {
			name          string
			event         string
			manifestAlive bool
		}{
			{name: "manifest removal", event: "remove:pending/" + manifestFileName, manifestAlive: true},
			{name: "manifest absence fsync", event: "sync-dir:pending", manifestAlive: false},
		} {
			t.Run(operation+"/"+failure.name, func(t *testing.T) {
				fixture := newFixture(t)
				writeOldAssets(t, fixture)
				if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
					t.Fatal(err)
				}
				fixture.files.resetEvents()
				fixture.files.failOn(failure.event, 1)
				var err error
				if operation == "commit" {
					err = fixture.installer.Commit(context.Background())
				} else {
					err = fixture.installer.Rollback(context.Background())
				}
				if err == nil {
					t.Fatal("operation succeeded despite injected cleanup crash point")
				}
				entries := readEntries(t, filepath.Join(fixture.assetRoot, pendingDirectoryName))
				want := []string{backupName(geoipName), backupName(geositeName)}
				if failure.manifestAlive {
					want = append(want, manifestFileName)
				}
				if !equalStrings(entries, want) {
					t.Fatalf("recovery entries = %v; want %v", entries, want)
				}
				fixture.files.failOn(failure.event)
				fixture.files.resetEvents()
				if operation == "commit" {
					err = fixture.installer.Commit(context.Background())
				} else {
					err = fixture.installer.Rollback(context.Background())
				}
				if err != nil {
					t.Fatalf("retry after cleanup crash point: %v", err)
				}
				if !failure.manifestAlive {
					retryEvents := fixture.files.eventsCopy()
					firstSync := indexOf(retryEvents, "sync-dir:pending")
					geositeRemoval := indexOf(retryEvents, "remove:pending/"+backupName(geositeName))
					geoipRemoval := indexOf(retryEvents, "remove:pending/"+backupName(geoipName))
					secondSync := indexWithOffset(retryEvents, "sync-dir:pending", firstSync+1)
					if firstSync < 0 || geositeRemoval <= firstSync || geoipRemoval <= firstSync ||
						secondSync <= geositeRemoval || secondSync <= geoipRemoval {
						t.Fatalf("orphan cleanup did not resync absence before removing backups: %v", retryEvents)
					}
				}
				if _, err := os.Lstat(filepath.Join(fixture.assetRoot, pendingDirectoryName)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("pending evidence remains after retry: %v", err)
				}
				if operation == "rollback" {
					assertOldAssets(t, fixture)
				}
			})
		}
	}
}

func TestCommitVerifiesLiveHashesAndRetriesPartialEvidenceCleanup(t *testing.T) {
	fixture := newFixture(t)
	writeOldAssets(t, fixture)
	if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
		t.Fatal(err)
	}
	fixture.files.failOn("remove:pending/"+backupName(geoipName), 1)
	if err := fixture.installer.Commit(context.Background()); !errors.Is(err, ErrInstallation) {
		t.Fatalf("Commit() error = %v; want injected cleanup error", err)
	}
	if err := fixture.installer.Commit(context.Background()); err != nil {
		t.Fatalf("retry Commit() error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.assetRoot, pendingDirectoryName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending evidence remains after commit: %v", err)
	}
	if err := fixture.installer.Commit(context.Background()); err != nil {
		t.Fatalf("idempotent Commit() error = %v", err)
	}

	drift := newFixture(t)
	if _, err := drift.installer.Install(context.Background(), drift.sources); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drift.assetRoot, geoipName), []byte("external edit"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := drift.installer.Commit(context.Background()); !errors.Is(err, ErrExternalDrift) {
		t.Fatalf("Commit() error = %v; want external drift", err)
	}
}

func TestVerifyWhileLockedRejectsLiveStandardAssetDrift(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.assetRoot, geoipName), []byte("external drift"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := fixture.installer.VerifyWhileLocked(context.Background()); !errors.Is(err, ErrExternalDrift) {
		t.Fatalf("VerifyWhileLocked() error = %v; want external drift", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.assetRoot, pendingDirectoryName, manifestFileName)); err != nil {
		t.Fatalf("verification changed recovery evidence: %v", err)
	}
}

func TestInstallReplacementBacksUpExactBytesModeAndOwner(t *testing.T) {
	fixture := newFixture(t)
	previous := map[string][]byte{
		geositeName: []byte("old geosite bytes\x00with exact trailing bytes\n"),
		geoipName:   []byte("old geoip bytes\xff"),
	}
	for index, name := range []string{geositeName, geoipName} {
		path := filepath.Join(fixture.assetRoot, name)
		if err := os.WriteFile(path, previous[name], 0600+os.FileMode(index*0040)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0600+os.FileMode(index*0040)); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := fixture.installer.Install(context.Background(), fixture.sources)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	pending := filepath.Join(fixture.assetRoot, pendingDirectoryName)
	for index, record := range manifest.Assets {
		name := []string{geositeName, geoipName}[index]
		if !record.PreviousExists || record.PreviousHash != hash(previous[name]) ||
			record.BackupName != backupName(name) {
			t.Errorf("record = %#v; want exact previous %s snapshot", record, name)
		}
		backup, err := os.ReadFile(filepath.Join(pending, backupName(name)))
		if err != nil || !bytes.Equal(backup, previous[name]) {
			t.Errorf("%s backup = %q, %v; want exact original bytes", name, backup, err)
		}
		backupInfo, err := os.Stat(filepath.Join(pending, backupName(name)))
		if err != nil || backupInfo.Mode().Perm() != privateFileMode {
			t.Errorf("%s backup mode = %v, %v; want 0600", name, backupInfo, err)
		}
		installedInfo, err := os.Stat(filepath.Join(fixture.assetRoot, name))
		if err != nil || installedInfo.Mode().Perm() != os.FileMode(0600+index*0040) {
			t.Errorf("%s installed mode = %v, %v", name, installedInfo, err)
		}
		assertOwner(t, installedInfo, os.Geteuid(), os.Getegid())
	}
}

func TestManifestAndBackupsAreSyncedBeforeTheFirstLiveRename(t *testing.T) {
	fixture := newFixture(t)
	writeOldAssets(t, fixture)
	fixture.files.resetEvents()
	if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
		t.Fatal(err)
	}
	events := fixture.files.eventsCopy()
	firstRename := indexOf(events, "rename:geosite.dat")
	if firstRename < 0 {
		t.Fatalf("rename events missing: %v", events)
	}
	lastPendingSync := lastIndexOf(events, "sync-dir:pending")
	if lastPendingSync < 0 || lastPendingSync >= firstRename {
		t.Fatalf("pending directory sync index %d must precede first rename %d: %v", lastPendingSync, firstRename, events)
	}
	geositeBackupSync := indexOf(events, "sync-file:pending/"+backupName(geositeName))
	geoipBackupSync := indexOf(events, "sync-file:pending/"+backupName(geoipName))
	manifestSync := indexWithPrefix(events, "sync-file:pending/"+manifestTempPrefix)
	manifestRename := indexOf(events, "rename:"+manifestFileName)
	if geositeBackupSync < 0 || geoipBackupSync < 0 ||
		manifestSync <= geositeBackupSync || manifestSync <= geoipBackupSync ||
		manifestRename <= manifestSync || manifestRename >= lastPendingSync ||
		lastPendingSync >= firstRename {
		t.Fatalf("expected both durable backups, then durable manifest and directory sync before first rename: %v", events)
	}
}

func TestPendingDirectorySyncFailurePreventsAnyLiveRename(t *testing.T) {
	fixture := newFixture(t)
	writeOldAssets(t, fixture)
	fixture.files.failOn("sync-dir:pending", 1)
	_, err := fixture.installer.Install(context.Background(), fixture.sources)
	if !errors.Is(err, ErrInstallation) {
		t.Fatalf("Install() error = %v; want manifest durability failure", err)
	}
	assertOldAssets(t, fixture)
	if _, err := os.Lstat(filepath.Join(fixture.assetRoot, pendingDirectoryName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending directory remains after pre-rename failure: %v", err)
	}
}

func TestInstallFailureBeforeOrBetweenRenamesRestoresAllFilesAndCleansEvidence(t *testing.T) {
	tests := []struct {
		name       string
		failTarget string
		setup      func(*testing.T, *fixture)
		assert     func(*testing.T, *fixture)
	}{
		{
			name:       "failure before first rename",
			failTarget: geositeName,
			setup:      writeOldAssets,
			assert:     assertOldAssets,
		},
		{
			name:       "failure between renames",
			failTarget: geoipName,
			setup:      writeOldAssets,
			assert:     assertOldAssets,
		},
		{
			name:       "initially absent assets are removed",
			failTarget: geoipName,
			assert:     assertNoLiveAssets,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			if test.setup != nil {
				test.setup(t, fixture)
			}
			fixture.files.failOn("rename:"+test.failTarget, 1)
			if _, err := fixture.installer.Install(context.Background(), fixture.sources); !errors.Is(err, ErrInstallation) {
				t.Fatalf("Install() error = %v; want installation failure", err)
			}
			test.assert(t, fixture)
			if _, err := os.Lstat(filepath.Join(fixture.assetRoot, pendingDirectoryName)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("pending artifacts remain after complete rollback: %v", err)
			}
			for _, name := range readEntries(t, fixture.assetRoot) {
				if strings.Contains(name, ".xch-candidate-") || strings.Contains(name, ".xch-restore-") {
					t.Errorf("temporary candidate remains: %s", name)
				}
			}
		})
	}
}

func TestFailedRestorationRetainsManifestAndBackups(t *testing.T) {
	fixture := newFixture(t)
	writeOldAssets(t, fixture)
	fixture.files.failOn("rename:"+geositeName, 1, 2)
	fixture.files.failOn("rename:"+geoipName, 1)
	_, recovery, err := fixture.installer.InstallOwnedWhileLocked(context.Background(), fixture.sources)
	if !errors.Is(err, ErrRestorationFailed) {
		t.Fatalf("Install() error = %v; want retained recovery evidence", err)
	}
	if recovery != ManifestRecovery {
		t.Fatalf("failed restoration recovery state = %d; want manifest recovery", recovery)
	}
	entries := readEntries(t, filepath.Join(fixture.assetRoot, pendingDirectoryName))
	if !equalStrings(entries, []string{backupName(geoipName), backupName(geositeName), manifestFileName}) {
		t.Fatalf("evidence after failed restoration = %v", entries)
	}
	manifest, readErr := fixture.installer.ReadPendingManifest()
	if readErr != nil || manifest == nil {
		t.Fatalf("pending manifest after failed restoration = %#v, %v", manifest, readErr)
	}
	for _, name := range []string{geositeName, geoipName} {
		backup, err := os.ReadFile(filepath.Join(fixture.assetRoot, pendingDirectoryName, backupName(name)))
		if err != nil || !bytes.Equal(backup, fixture.old[name]) {
			t.Errorf("%s recovery backup = %q, %v", name, backup, err)
		}
	}
}

func TestCleanupFailureIsReturnedWithoutReportingSuccess(t *testing.T) {
	fixture := newFixture(t)
	writeOldAssets(t, fixture)
	fixture.files.failOn("rename:"+geositeName, 1)
	fixture.files.failOn("remove:pending/"+manifestFileName, 1)
	if _, err := fixture.installer.Install(context.Background(), fixture.sources); !errors.Is(err, ErrInstallation) {
		t.Fatalf("Install() error = %v; want cleanup failure", err)
	}
	entries := readEntries(t, filepath.Join(fixture.assetRoot, pendingDirectoryName))
	if !equalStrings(entries, []string{backupName(geoipName), backupName(geositeName), manifestFileName}) {
		t.Fatalf("cleanup removed recovery artifacts after manifest-remove failure: %v", entries)
	}
	events := fixture.files.eventsCopy()
	assertOrderedEvents(t, events,
		"sync-dir:pending",
		"remove:pending/"+manifestFileName,
	)
	manifestRemoval := indexOf(events, "remove:pending/"+manifestFileName)
	if indexOf(events[manifestRemoval+1:], "sync-dir:pending") >= 0 {
		t.Fatalf("unexpected pending-directory sync after failed manifest removal: %v", events)
	}
	if indexOf(events, "remove:pending/"+backupName(geositeName)) >= 0 ||
		indexOf(events, "remove:pending/"+backupName(geoipName)) >= 0 {
		t.Fatalf("backup was deleted while manifest removal failed: %v", events)
	}
	if err := fixture.installer.Rollback(context.Background()); err != nil {
		t.Fatalf("retry rollback/cleanup after manifest removal failure: %v", err)
	}
	assertOldAssets(t, fixture)
}

func TestCleanupSyncFailureAfterManifestRemovalKeepsAllBackups(t *testing.T) {
	fixture := newFixture(t)
	writeOldAssets(t, fixture)
	fixture.files.failOn("rename:"+geositeName, 1)
	// The pending directory is synced after manifest publication and once after
	// durable manifest removal, before any backup is removed.
	fixture.files.failOn("sync-dir:pending", 2)
	_, err := fixture.installer.Install(context.Background(), fixture.sources)
	if !errors.Is(err, ErrInstallation) {
		t.Fatalf("Install() error = %v; want cleanup fsync failure", err)
	}
	entries := readEntries(t, filepath.Join(fixture.assetRoot, pendingDirectoryName))
	if !equalStrings(entries, []string{backupName(geoipName), backupName(geositeName)}) {
		t.Fatalf("backups were not retained after manifest-removal fsync failure: %v", entries)
	}
	events := fixture.files.eventsCopy()
	assertOrderedEvents(t, events,
		"remove:pending/"+manifestFileName,
		"sync-dir:pending",
	)
	if err := fixture.installer.Rollback(context.Background()); err != nil {
		t.Fatalf("retry rollback/cleanup after directory sync failure: %v", err)
	}
	assertOldAssets(t, fixture)
}

func TestCleanupPartialBackupRemovalOccursOnlyAfterDurableManifestRemoval(t *testing.T) {
	fixture := newFixture(t)
	writeOldAssets(t, fixture)
	fixture.files.failOn("rename:"+geositeName, 1)
	fixture.files.failOn("remove:pending/"+backupName(geoipName), 1)
	_, err := fixture.installer.Install(context.Background(), fixture.sources)
	if !errors.Is(err, ErrInstallation) {
		t.Fatalf("Install() error = %v; want partial backup cleanup failure", err)
	}
	entries := readEntries(t, filepath.Join(fixture.assetRoot, pendingDirectoryName))
	if !equalStrings(entries, []string{backupName(geoipName)}) {
		t.Fatalf("remaining backup after partial cleanup = %v", entries)
	}
	events := fixture.files.eventsCopy()
	assertOrderedEvents(t, events,
		"remove:pending/"+manifestFileName,
		"sync-dir:pending",
		"remove:pending/"+backupName(geositeName),
		"remove:pending/"+backupName(geoipName),
		"sync-dir:pending",
	)
	if err := fixture.installer.Rollback(context.Background()); err != nil {
		t.Fatalf("retry rollback/cleanup after partial backup deletion: %v", err)
	}
	assertOldAssets(t, fixture)
}

func TestInstallRejectsUnsafeTargetsAndInputAliases(t *testing.T) {
	t.Run("active manifest", func(t *testing.T) {
		fixture := newFixture(t)
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); !errors.Is(err, ErrPendingManifest) {
			t.Fatalf("second Install() error = %v; want active-manifest rejection", err)
		}
	})
	t.Run("active manifest symlink", func(t *testing.T) {
		fixture := newFixture(t)
		pending := filepath.Join(fixture.assetRoot, pendingDirectoryName)
		if err := os.Mkdir(pending, 0700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), manifestFileName)
		if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(pending, manifestFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); !errors.Is(err, ErrPendingManifest) {
			t.Fatalf("Install() error = %v; want active-manifest rejection", err)
		}
		got, err := os.ReadFile(outside)
		if err != nil || string(got) != "outside" {
			t.Fatalf("outside manifest target changed: %q, %v", got, err)
		}
	})
	t.Run("symlink target", func(t *testing.T) {
		fixture := newFixture(t)
		target := filepath.Join(t.TempDir(), "external.dat")
		if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(fixture.assetRoot, geositeName)); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); !errors.Is(err, ErrUnsafeFilesystem) {
			t.Fatalf("Install() error = %v; want symlink rejection", err)
		}
	})
	t.Run("non-regular target", func(t *testing.T) {
		fixture := newFixture(t)
		path := filepath.Join(fixture.assetRoot, geoipName)
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); !errors.Is(err, ErrUnsafeFilesystem) {
			t.Fatalf("Install() error = %v; want non-regular file rejection", err)
		}
	})
	t.Run("pending directory symlink", func(t *testing.T) {
		fixture := newFixture(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(fixture.assetRoot, pendingDirectoryName)); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); !errors.Is(err, ErrUnsafeFilesystem) {
			t.Fatalf("Install() error = %v; want pending-directory symlink rejection", err)
		}
		if entries := readEntries(t, outside); len(entries) != 0 {
			t.Fatalf("outside symlink target was modified: %v", entries)
		}
	})
	t.Run("pending directory permissions", func(t *testing.T) {
		fixture := newFixture(t)
		pending := filepath.Join(fixture.assetRoot, pendingDirectoryName)
		if err := os.Mkdir(pending, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(pending, 0777); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); !errors.Is(err, ErrUnsafeFilesystem) {
			t.Fatalf("Install() error = %v; want insecure pending-directory rejection", err)
		}
		info, err := os.Stat(pending)
		if err != nil || info.Mode().Perm() != 0777 {
			t.Fatalf("existing unsafe directory was changed: mode=%v err=%v", info, err)
		}
	})
	t.Run("asset directory symlink", func(t *testing.T) {
		fixture := newFixture(t)
		realDirectory := fixture.assetRoot + "-real"
		if err := os.Rename(fixture.assetRoot, realDirectory); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realDirectory, fixture.assetRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); !errors.Is(err, ErrInstallation) {
			t.Fatalf("Install() error = %v; want asset-directory symlink rejection", err)
		}
		if entries := readEntries(t, realDirectory); len(entries) != 0 {
			t.Fatalf("real asset directory behind symlink was modified: %v", entries)
		}
	})
	t.Run("source symlink", func(t *testing.T) {
		fixture := newFixture(t)
		target := filepath.Join(fixture.sourceRoot, "actual-geosite")
		if err := os.Rename(fixture.sources.GeositePath, target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, fixture.sources.GeositePath); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); !errors.Is(err, ErrInstallation) {
			t.Fatalf("Install() error = %v; want symlink-source rejection", err)
		}
	})
	t.Run("source aliases live target", func(t *testing.T) {
		fixture := newFixture(t)
		if err := os.WriteFile(filepath.Join(fixture.assetRoot, geositeName), fixture.sourceBytes[0], 0600); err != nil {
			t.Fatal(err)
		}
		fixture.sources.GeositePath = filepath.Join(fixture.assetRoot, geositeName)
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Install() error = %v; want alias rejection", err)
		}
	})
	t.Run("hard-linked sources", func(t *testing.T) {
		fixture := newFixture(t)
		if err := os.Remove(fixture.sources.GeoipPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(fixture.sources.GeositePath, fixture.sources.GeoipPath); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Install() error = %v; want hard-link source alias rejection", err)
		}
	})
	t.Run("hard-linked live assets", func(t *testing.T) {
		fixture := newFixture(t)
		geosite := filepath.Join(fixture.assetRoot, geositeName)
		geoip := filepath.Join(fixture.assetRoot, geoipName)
		if err := os.WriteFile(geosite, []byte("preexisting alias"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(geosite, geoip); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.installer.Install(context.Background(), fixture.sources); !errors.Is(err, ErrUnsafeFilesystem) {
			t.Fatalf("Install() error = %v; want live-asset alias rejection", err)
		}
	})
	t.Run("unvalidated name and asset directory", func(t *testing.T) {
		fixture := newFixture(t)
		if _, err := fixture.installer.Install(context.Background(), Sources{
			GeositePath: filepath.Join(fixture.sourceRoot, "other.dat"),
			GeoipPath:   fixture.sources.GeoipPath,
		}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Install() accepted a non-canonical source path: %v", err)
		}
		if _, err := New(t.TempDir(), nil); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("New() accepted an arbitrary asset directory: %v", err)
		}
	})
}

func TestReadPendingManifestUsesBoundedStrictJSON(t *testing.T) {
	fixture := newFixture(t)
	writeOldAssets(t, fixture)
	manifest, err := fixture.installer.Install(context.Background(), fixture.sources)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(filepath.Join(fixture.assetRoot, pendingDirectoryName, manifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	valid := string(encoded)
	for _, invalid := range []string{
		strings.Replace(valid, `"version":1`, `"version":1,"unknown":true`, 1),
		strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(valid, `,"previousExists":true`, "", 1),
		strings.Replace(valid, `"previousExists":true`, `"previousExists":null`, 1),
		valid + `{}`,
		`{}`,
		strings.Repeat(" ", maxManifestSize+1),
	} {
		if _, err := decodeManifest([]byte(invalid)); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("decodeManifest() accepted invalid strict JSON: %v", err)
		}
	}
	if err := os.Chmod(filepath.Join(fixture.assetRoot, pendingDirectoryName, manifestFileName), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.installer.ReadPendingManifest(); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("ReadPendingManifest() accepted a non-private manifest: %v", err)
	}
	if manifest.Version != manifestVersion {
		t.Fatalf("fixture manifest = %#v", manifest)
	}
}

func TestReadPendingManifestRejectsSymlinkedOrCorruptBackups(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, string)
	}{
		{
			name: "symlink backup",
			mutate: func(t *testing.T, backup, outside string) {
				t.Helper()
				if err := os.Remove(backup); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, backup); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "corrupt backup",
			mutate: func(t *testing.T, backup, _ string) {
				t.Helper()
				if err := os.WriteFile(backup, []byte("different previous bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			writeOldAssets(t, fixture)
			if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
				t.Fatal(err)
			}
			backup := filepath.Join(fixture.assetRoot, pendingDirectoryName, backupName(geositeName))
			outside := filepath.Join(t.TempDir(), "outside-backup")
			if err := os.WriteFile(outside, fixture.old[geositeName], 0600); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, backup, outside)
			if _, err := fixture.installer.ReadPendingManifest(); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("ReadPendingManifest() error = %v; want invalid-backup rejection", err)
			}
		})
	}
}

func TestInspectClassifiesPersistedStandardEvidenceReadOnly(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, *fixture)
		phase InspectionPhase
		live  [2]LiveAssetPhase
	}{
		{name: "first install candidate live", setup: func(t *testing.T, fixture *fixture) {
			t.Helper()
			if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
				t.Fatal(err)
			}
		}, phase: InspectionManifest, live: [2]LiveAssetPhase{LiveAssetInstalled, LiveAssetInstalled}},
		{name: "manifest absent cleanup only", setup: func(t *testing.T, fixture *fixture) {
			t.Helper()
			writeOldAssets(t, fixture)
			if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(fixture.assetRoot, pendingDirectoryName, manifestFileName)); err != nil {
				t.Fatal(err)
			}
		}, phase: InspectionCleanupOnly},
		{name: "malformed duplicate-key manifest", setup: func(t *testing.T, fixture *fixture) {
			t.Helper()
			if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(fixture.assetRoot, pendingDirectoryName, manifestFileName)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.Replace(data, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
		}, phase: InspectionInvalid},
		{name: "missing required backup", setup: func(t *testing.T, fixture *fixture) {
			t.Helper()
			writeOldAssets(t, fixture)
			if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(fixture.assetRoot, pendingDirectoryName, backupName(geositeName))); err != nil {
				t.Fatal(err)
			}
		}, phase: InspectionInvalid},
		{name: "symlink backup", setup: func(t *testing.T, fixture *fixture) {
			t.Helper()
			writeOldAssets(t, fixture)
			if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(fixture.assetRoot, pendingDirectoryName, backupName(geositeName))
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(fixture.sources.GeositePath, path); err != nil {
				t.Fatal(err)
			}
		}, phase: InspectionInvalid},
		{name: "extra pending entry", setup: func(t *testing.T, fixture *fixture) {
			t.Helper()
			if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixture.assetRoot, pendingDirectoryName, "unexpected"), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
		}, phase: InspectionInvalid},
		{name: "external live drift", setup: func(t *testing.T, fixture *fixture) {
			t.Helper()
			if _, err := fixture.installer.Install(context.Background(), fixture.sources); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixture.assetRoot, geositeName), []byte("external"), 0644); err != nil {
				t.Fatal(err)
			}
		}, phase: InspectionManifest, live: [2]LiveAssetPhase{LiveAssetDrift, LiveAssetInstalled}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			test.setup(t, fixture)
			fixture.files.resetEvents()
			first, err := fixture.installer.Inspect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			second, err := fixture.installer.Inspect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if first.Phase != test.phase || first.Phase != second.Phase || first.Live != second.Live ||
				first.Live != test.live || len(fixture.files.eventsCopy()) != 0 {
				t.Fatalf("Inspect() = %#v then %#v; want phase=%v live=%v and no writes",
					first, second, test.phase, test.live)
			}
		})
	}
}

type fixture struct {
	assetRoot   string
	sourceRoot  string
	sourceBytes [2][]byte
	sources     Sources
	installer   *Installer
	files       *injectedFileSystem
	old         map[string][]byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	assetRoot := filepath.Join(root, "assets")
	sourceRoot := filepath.Join(root, "download")
	if err := os.Mkdir(assetRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sourceRoot, 0700); err != nil {
		t.Fatal(err)
	}
	bytesForAsset := [2][]byte{
		bytes.Repeat([]byte("geosite data\n"), 100),
		bytes.Repeat([]byte("geoip data\n"), 100),
	}
	sources := Sources{
		GeositePath: filepath.Join(sourceRoot, geositeName),
		GeoipPath:   filepath.Join(sourceRoot, geoipName),
	}
	for index, path := range []string{sources.GeositePath, sources.GeoipPath} {
		if err := os.WriteFile(path, bytesForAsset[index], 0600); err != nil {
			t.Fatal(err)
		}
	}
	files := &injectedFileSystem{
		osFileSystem: osFileSystem{},
		assetPath:    testAssetDir,
		assetRoot:    assetRoot,
		failures:     make(map[string]map[int]bool),
		hits:         make(map[string]int),
	}
	installer, err := New(testAssetDir, files)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		assetRoot:   assetRoot,
		sourceRoot:  sourceRoot,
		sourceBytes: bytesForAsset,
		sources:     sources,
		installer:   installer,
		files:       files,
		old:         make(map[string][]byte),
	}
}

func writeOldAssets(t *testing.T, fixture *fixture) {
	t.Helper()
	fixture.old[geositeName] = []byte("old geosite bytes\x00with exact trailing bytes\n")
	fixture.old[geoipName] = []byte("old geoip bytes\xff")
	for index, name := range []string{geositeName, geoipName} {
		path := filepath.Join(fixture.assetRoot, name)
		if err := os.WriteFile(path, fixture.old[name], 0600+os.FileMode(index*0040)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0600+os.FileMode(index*0040)); err != nil {
			t.Fatal(err)
		}
	}
}

func assertOldAssets(t *testing.T, fixture *fixture) {
	t.Helper()
	for index, name := range []string{geositeName, geoipName} {
		path := filepath.Join(fixture.assetRoot, name)
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, fixture.old[name]) {
			t.Errorf("%s = %q, %v; want exact previous bytes", name, got, err)
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != os.FileMode(0600+index*0040) {
			t.Errorf("%s mode = %v, %v; want exact previous mode", name, info, err)
		}
		assertOwner(t, info, os.Geteuid(), os.Getegid())
	}
}

func assertNoLiveAssets(t *testing.T, fixture *fixture) {
	t.Helper()
	for _, name := range []string{geositeName, geoipName} {
		if _, err := os.Lstat(filepath.Join(fixture.assetRoot, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after rollback of initial install: %v", name, err)
		}
	}
}

func assertOwner(t *testing.T, info os.FileInfo, uid, gid int) {
	t.Helper()
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid || int(stat.Gid) != gid {
		t.Fatalf("owner = %#v; want uid=%d gid=%d", info.Sys(), uid, gid)
	}
}

func readEntries(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func hash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}

func indexWithPrefix(values []string, prefix string) int {
	for index, value := range values {
		if strings.HasPrefix(value, prefix) {
			return index
		}
	}
	return -1
}

func indexWithOffset(values []string, target string, start int) int {
	for index := start; index < len(values); index++ {
		if values[index] == target {
			return index
		}
	}
	return -1
}

func lastIndexOf(values []string, target string) int {
	for index := len(values) - 1; index >= 0; index-- {
		if values[index] == target {
			return index
		}
	}
	return -1
}

func assertOrderedEvents(t *testing.T, events []string, expected ...string) {
	t.Helper()
	next := 0
	for _, event := range events {
		if event == expected[next] {
			next++
			if next == len(expected) {
				return
			}
		}
	}
	t.Fatalf("events do not contain ordered sequence %v: %v", expected, events)
}

type injectedFileSystem struct {
	osFileSystem
	assetPath string
	assetRoot string
	mu        sync.Mutex
	events    []string
	failures  map[string]map[int]bool
	hits      map[string]int
}

func (files *injectedFileSystem) OpenDir(path string) (Directory, error) {
	label := "source"
	if path == files.assetPath {
		path, label = files.assetRoot, "asset"
	}
	directory, err := files.osFileSystem.OpenDir(path)
	if err != nil {
		return nil, err
	}
	return &injectedDirectory{Directory: directory, files: files, label: label}, nil
}

func (files *injectedFileSystem) failOn(operation string, occurrences ...int) {
	files.mu.Lock()
	defer files.mu.Unlock()
	files.failures[operation] = make(map[int]bool)
	for _, occurrence := range occurrences {
		files.failures[operation][occurrence] = true
	}
}

func (files *injectedFileSystem) record(operation string) error {
	files.mu.Lock()
	defer files.mu.Unlock()
	files.events = append(files.events, operation)
	files.hits[operation]++
	if files.failures[operation][files.hits[operation]] {
		return fmt.Errorf("injected failure at %s", operation)
	}
	return nil
}

func (files *injectedFileSystem) resetEvents() {
	files.mu.Lock()
	defer files.mu.Unlock()
	files.events = nil
	files.hits = make(map[string]int)
}

func (files *injectedFileSystem) eventsCopy() []string {
	files.mu.Lock()
	defer files.mu.Unlock()
	return append([]string(nil), files.events...)
}

type injectedDirectory struct {
	Directory
	files *injectedFileSystem
	label string
}

func (directory *injectedDirectory) ReadDir() ([]string, error) {
	return directory.Directory.ReadDir()
}

func (directory *injectedDirectory) Mkdir(name string, mode os.FileMode) error {
	return directory.Directory.Mkdir(name, mode)
}

func (directory *injectedDirectory) OpenDir(name string) (Directory, error) {
	opened, err := directory.Directory.OpenDir(name)
	if err != nil {
		return nil, err
	}
	label := name
	if directory.label == "asset" && name == pendingDirectoryName {
		label = "pending"
	}
	return &injectedDirectory{Directory: opened, files: directory.files, label: label}, nil
}

func (directory *injectedDirectory) OpenFile(name string, flags int, mode os.FileMode) (File, error) {
	file, err := directory.Directory.OpenFile(name, flags, mode)
	if err != nil {
		return nil, err
	}
	return &injectedFile{File: file, files: directory.files, label: directory.label + "/" + name}, nil
}

func (directory *injectedDirectory) Rename(oldName, newName string) error {
	if err := directory.files.record("rename:" + newName); err != nil {
		return err
	}
	return directory.Directory.Rename(oldName, newName)
}

func (directory *injectedDirectory) Remove(name string) error {
	if err := directory.files.record("remove:" + directory.label + "/" + name); err != nil {
		return err
	}
	return directory.Directory.Remove(name)
}

func (directory *injectedDirectory) Sync() error {
	if err := directory.files.record("sync-dir:" + directory.label); err != nil {
		return err
	}
	return directory.Directory.Sync()
}

type injectedFile struct {
	File
	files *injectedFileSystem
	label string
}

func (file *injectedFile) Sync() error {
	if err := file.files.record("sync-file:" + file.label); err != nil {
		return err
	}
	return file.File.Sync()
}

func TestConfiguredAssetLimitPreventsInstallation(t *testing.T) {
	fixture := newFixture(t)
	installer, err := New(testAssetDir, fixture.files, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(fixture.sources.GeositePath, (1<<20)+1); err != nil {
		t.Fatal(err)
	}
	_, err = installer.Install(context.Background(), fixture.sources)
	var size *geodatalimits.Exceeded
	if !errors.As(err, &size) || size.LimitBytes != 1<<20 {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fixture.assetRoot, geositeName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oversized asset was installed", err)
	}
}
