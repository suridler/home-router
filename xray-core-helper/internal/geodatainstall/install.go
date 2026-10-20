// Package geodatainstall installs only the two fixed, validated Xray geodata
// assets and retains a durable manifest and backups for later recovery.
package geodatainstall

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/geodatalimits"
)

const (
	manifestVersion = 1
	maxManifestSize = 16 << 10
	privateDirMode  = 0700
	privateFileMode = 0600
	installedMode   = 0644

	pendingDirectoryName = ".xray-core-helper-pending"
	manifestFileName     = "manifest.json"
	manifestTempPrefix   = ".manifest.json.xch-candidate-"
	geositeName          = "geosite.dat"
	geoipName            = "geoip.dat"
)

var (
	ErrInvalidInput      = errors.New("invalid geodata installation input")
	ErrPendingManifest   = errors.New("a geodata installation is already pending")
	ErrRecoveryRequired  = errors.New("geodata installation recovery is required")
	ErrUnsafeFilesystem  = errors.New("geodata installation encountered an unsafe filesystem entry")
	ErrExternalDrift     = errors.New("geodata assets changed outside installation")
	ErrInstallation      = errors.New("geodata installation failed")
	ErrRestorationFailed = errors.New("geodata files could not be fully restored")
	ErrInvalidManifest   = errors.New("pending geodata manifest is invalid")
)

type Sources struct {
	GeositePath string
	GeoipPath   string
}

type AssetRecord struct {
	Name           string `json:"name"`
	InstalledHash  string `json:"installedHash"`
	PreviousExists bool   `json:"previousExists"`
	PreviousHash   string `json:"previousHash"`
	BackupName     string `json:"backupName"`
	PreviousMode   uint32 `json:"previousMode"`
	PreviousUID    int    `json:"previousUID"`
	PreviousGID    int    `json:"previousGID"`
}

type Manifest struct {
	Version int            `json:"version"`
	ID      string         `json:"id"`
	Assets  [2]AssetRecord `json:"assets"`
}

type Installer struct {
	maxAssetSize int
	assetDir     string
	files        FileSystem
}

// InstallFailureError describes whether a failed install left a valid recovery
// manifest created by that install invocation.
type InstallFailureError struct {
	Recovery InstallRecovery
	Err      error
}

type InstallRecovery uint8

const (
	NoInstallRecovery InstallRecovery = iota
	ManifestRecovery
	CleanupOnlyRecovery
)

func (failure *InstallFailureError) Error() string {
	return failure.Err.Error()
}

func (failure *InstallFailureError) Unwrap() error {
	return failure.Err
}

type stagedAsset struct {
	name          string
	candidate     string
	candidateMade bool
	backupMade    bool
}

// New creates an installer restricted to one of the two configured asset
// directories. A nil filesystem selects the no-follow operating-system one.
func New(assetDir string, files FileSystem, limitMB ...int) (*Installer, error) {
	if assetDir != "/usr/local/share/xray" && assetDir != "/var/lib/xray-core-helper/geodata" {
		return nil, ErrInvalidInput
	}
	if files == nil {
		files = osFileSystem{}
	}
	limit := geodatalimits.DefaultAssetSize
	if len(limitMB) > 0 {
		if !geodatalimits.ValidMB(limitMB[0]) {
			return nil, ErrInvalidInput
		}
		limit = geodatalimits.Bytes(limitMB[0])
	}
	return &Installer{assetDir: assetDir, files: files, maxAssetSize: limit}, nil
}

// Install atomically replaces geosite.dat and geoip.dat with the supplied
// validated assets. It never restarts Xray or changes domain state.
func (installer *Installer) Install(ctx context.Context, sources Sources) (Manifest, error) {
	if ctx == nil {
		return Manifest{}, ErrInvalidInput
	}
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return Manifest{}, err
	}
	defer lease.Release()
	return installer.InstallWhileLocked(ctx, sources)
}

// InstallWhileLocked installs assets when the caller already owns the process
// mutation coordinator. Callers must retain the lease until the transaction
// finishes.
func (installer *Installer) InstallWhileLocked(ctx context.Context, sources Sources) (Manifest, error) {
	if ctx == nil || !validSourcePath(sources.GeositePath, installer.assetDir) ||
		!validSourcePath(sources.GeoipPath, installer.assetDir) ||
		filepath.Base(sources.GeositePath) != geositeName ||
		filepath.Base(sources.GeoipPath) != geoipName ||
		filepath.Clean(sources.GeositePath) == filepath.Clean(sources.GeoipPath) {
		return Manifest{}, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}

	assetDirectory, err := installer.files.OpenDir(installer.assetDir)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: open configured asset directory", ErrInstallation)
	}
	defer assetDirectory.Close()
	pendingDirectory, err := installer.openPendingDirectory(assetDirectory, true, false)
	if err != nil {
		return Manifest{}, err
	}
	defer pendingDirectory.Close()

	id, err := randomID()
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: create transaction identifier", ErrInstallation)
	}
	names := [2]string{geositeName, geoipName}
	sourcePaths := [2]string{sources.GeositePath, sources.GeoipPath}
	sourceFiles := [2]File{}
	sourceInfos := [2]os.FileInfo{}
	for index, path := range sourcePaths {
		sourceFiles[index], err = installer.files.OpenSource(path)
		if err != nil {
			closeFiles(sourceFiles[:])
			return Manifest{}, fmt.Errorf("%w: open validated asset", ErrInstallation)
		}
		sourceInfos[index], err = sourceFiles[index].Stat()
		if err != nil || sourceInfos[index] == nil || !sourceInfos[index].Mode().IsRegular() {
			closeFiles(sourceFiles[:])
			return Manifest{}, ErrUnsafeFilesystem
		}
	}
	defer closeFiles(sourceFiles[:])
	if fileIdentity(sourceInfos[0], sourceInfos[1]) {
		return Manifest{}, ErrInvalidInput
	}

	records := [2]AssetRecord{}
	staged := [2]stagedAsset{}
	manifestCreated := false
	replacementsStarted := false
	manifest := Manifest{}
	manifestCandidate := manifestTempPrefix + id
	cleanupStaged := func() error {
		return cleanupStagedFiles(assetDirectory, pendingDirectory, staged, records, manifestCreated, manifestCandidate)
	}
	fail := func(cause error) (Manifest, error) {
		if replacementsStarted {
			restoreErr := installer.restoreAssets(assetDirectory, pendingDirectory, records)
			if restoreErr != nil {
				cleanupErr := cleanupCandidates(assetDirectory, staged)
				return Manifest{}, &InstallFailureError{Recovery: ManifestRecovery,
					Err: errors.Join(ErrInstallation, ErrRestorationFailed, cause, restoreErr, cleanupErr)}
			}
		}
		cleanupErr := cleanupStaged()
		recovery := NoInstallRecovery
		if cleanupErr != nil {
			if manifestCreated {
				_, statErr := pendingDirectory.Lstat(manifestFileName)
				if statErr == nil {
					recovery = ManifestRecovery
				} else if errors.Is(statErr, os.ErrNotExist) {
					recovery = CleanupOnlyRecovery
				}
			} else {
				recovery = CleanupOnlyRecovery
			}
		}
		return Manifest{}, &InstallFailureError{Recovery: recovery,
			Err: errors.Join(ErrInstallation, cause, cleanupErr)}
	}
	for index, name := range names {
		staged[index].name = name
		staged[index].candidate = "." + name + ".xch-candidate-" + id
	}
	for index := range names {
		if err := installer.rejectSourceAliases(sourcePaths[index], sourceInfos[index], assetDirectory, names); err != nil {
			return Manifest{}, err
		}
	}
	if err := rejectLiveAssetAliases(assetDirectory, names); err != nil {
		return Manifest{}, err
	}
	for index, name := range names {
		records[index].Name = name
		records[index], staged[index].backupMade, err = installer.snapshotAndBackup(ctx, assetDirectory, pendingDirectory, name, records[index])
		if err != nil {
			return fail(fmt.Errorf("snapshot previous asset: %w", err))
		}
	}

	for index := range names {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		made, hash, writeErr := installer.writeCandidate(ctx, assetDirectory, staged[index].candidate, sourceFiles[index], sourceInfos[index])
		staged[index].candidateMade = made
		if writeErr != nil {
			return fail(writeErr)
		}
		records[index].InstalledHash = hash
	}
	if err := assetDirectory.Sync(); err != nil {
		return fail(err)
	}

	manifest = Manifest{Version: manifestVersion, ID: id, Assets: records}
	if err := validateManifest(manifest); err != nil {
		return fail(err)
	}
	manifestCreated, err = writeManifest(pendingDirectory, manifest, manifestCandidate)
	if err != nil {
		return fail(err)
	}
	if err := pendingDirectory.Sync(); err != nil {
		return fail(err)
	}

	for index := range staged {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		record := records[index]
		if err := applyInstallPolicy(assetDirectory, staged[index].candidate, record); err != nil {
			return fail(err)
		}
		replacementsStarted = true
		if err := assetDirectory.Rename(staged[index].candidate, staged[index].name); err != nil {
			return fail(err)
		}
		staged[index].candidateMade = false
		if err := assetDirectory.Sync(); err != nil {
			return fail(err)
		}
	}
	return manifest, nil
}

// InstallOwnedWhileLocked reports transaction ownership explicitly for a
// coordinator-owning caller. A successful install owns its durable manifest;
// a failed install owns recovery only when its typed failure says so.
func (installer *Installer) InstallOwnedWhileLocked(ctx context.Context, sources Sources) (Manifest, InstallRecovery, error) {
	manifest, err := installer.InstallWhileLocked(ctx, sources)
	if err == nil {
		return manifest, ManifestRecovery, nil
	}
	var failure *InstallFailureError
	if errors.As(err, &failure) {
		return Manifest{}, failure.Recovery, err
	}
	return Manifest{}, NoInstallRecovery, err
}

// ReadPendingManifest reads and strictly validates the fixed active manifest.
func (installer *Installer) ReadPendingManifest() (*Manifest, error) {
	return installer.readPendingManifest()
}

type InspectionPhase uint8

const (
	InspectionClean InspectionPhase = iota
	InspectionManifest
	InspectionCleanupOnly
	InspectionInvalid
)

type LiveAssetPhase uint8

const (
	LiveAssetMissing LiveAssetPhase = iota
	LiveAssetInstalled
	LiveAssetPrevious
	LiveAssetDrift
)

type Inspection struct {
	Phase    InspectionPhase
	Manifest *Manifest
	Live     [2]LiveAssetPhase
}

// Inspect reads the active transaction evidence and live asset hashes without
// changing files. Manifest parsing and backup checks use the installer's
// existing strict, no-follow validation.
func (installer *Installer) Inspect(ctx context.Context) (Inspection, error) {
	if ctx == nil {
		return Inspection{}, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	assetDirectory, err := installer.files.OpenDir(installer.assetDir)
	if err != nil {
		return Inspection{}, fmt.Errorf("%w: open configured asset directory", ErrInstallation)
	}
	defer assetDirectory.Close()
	pendingDirectory, err := installer.openPendingDirectory(assetDirectory, false, true)
	if errors.Is(err, os.ErrNotExist) {
		return Inspection{Phase: InspectionClean}, nil
	}
	if err != nil {
		return Inspection{Phase: InspectionInvalid}, nil
	}
	defer pendingDirectory.Close()

	manifestInfo, err := pendingDirectory.Lstat(manifestFileName)
	if errors.Is(err, os.ErrNotExist) {
		entries, readErr := pendingDirectory.ReadDir()
		if readErr != nil {
			return Inspection{Phase: InspectionInvalid}, nil
		}
		for _, entry := range entries {
			info, statErr := pendingDirectory.Lstat(entry)
			if statErr != nil || info == nil || !info.Mode().IsRegular() ||
				mode(info) != uint32(privateFileMode) {
				return Inspection{Phase: InspectionInvalid}, nil
			}
			uid, _, ownerErr := owner(info)
			if ownerErr != nil || uid != os.Geteuid() {
				return Inspection{Phase: InspectionInvalid}, nil
			}
			file, openErr := pendingDirectory.OpenFile(entry, os.O_RDONLY, 0)
			if openErr != nil {
				return Inspection{Phase: InspectionInvalid}, nil
			}
			openedInfo, statErr := file.Stat()
			size, readErr := io.Copy(io.Discard, io.LimitReader(file, int64(installer.maxAssetSize)+1))
			closeErr := file.Close()
			if statErr != nil || !fileIdentity(info, openedInfo) ||
				readErr != nil || closeErr != nil || size > int64(installer.maxAssetSize) {
				return Inspection{Phase: InspectionInvalid}, nil
			}
		}
		return Inspection{Phase: InspectionCleanupOnly}, nil
	}
	if err != nil || manifestInfo == nil {
		return Inspection{Phase: InspectionInvalid}, nil
	}
	manifest, err := installer.readManifest(pendingDirectory)
	if err != nil {
		return Inspection{Phase: InspectionInvalid}, nil
	}
	inspection := Inspection{Phase: InspectionManifest, Manifest: &manifest}
	for index, record := range manifest.Assets {
		hash, exists, hashErr := installer.hashOptionalAssetContext(ctx, assetDirectory, record.Name)
		if hashErr != nil {
			return Inspection{Phase: InspectionInvalid}, nil
		}
		switch {
		case !exists:
			inspection.Live[index] = LiveAssetMissing
		case hash == record.InstalledHash:
			inspection.Live[index] = LiveAssetInstalled
		case record.PreviousExists && hash == record.PreviousHash:
			if verifyPreviousMetadata(assetDirectory, record) != nil {
				return Inspection{Phase: InspectionInvalid}, nil
			}
			inspection.Live[index] = LiveAssetPrevious
		default:
			inspection.Live[index] = LiveAssetDrift
		}
	}
	return inspection, nil
}

// Rollback restores the exact previous standard assets, or their absence,
// only while every live asset still matches the installed or previous hash.
func (installer *Installer) Rollback(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	return installer.RollbackWhileLocked(ctx)
}

// RollbackWhileLocked is Rollback for callers already holding the coordinator.
func (installer *Installer) RollbackWhileLocked(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	assetDirectory, err := installer.files.OpenDir(installer.assetDir)
	if err != nil {
		return fmt.Errorf("%w: open configured asset directory", ErrInstallation)
	}
	defer assetDirectory.Close()
	pendingDirectory, err := installer.openPendingDirectory(assetDirectory, false, true)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer pendingDirectory.Close()
	entries, err := pendingDirectory.ReadDir()
	if err != nil {
		return ErrUnsafeFilesystem
	}
	manifestInfo, manifestErr := pendingDirectory.Lstat(manifestFileName)
	if errors.Is(manifestErr, os.ErrNotExist) {
		return cleanupOrphanEvidence(assetDirectory, pendingDirectory, entries, ErrRestorationFailed)
	}
	if manifestErr != nil || manifestInfo == nil {
		return ErrInvalidManifest
	}
	manifest, err := installer.readManifest(pendingDirectory)
	if err != nil {
		return err
	}
	if err := installer.verifyLiveAssets(assetDirectory, manifest, true); err != nil {
		return err
	}
	for index := len(manifest.Assets) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return errors.Join(ErrRestorationFailed, err)
		}
		record := manifest.Assets[index]
		hash, exists, err := installer.hashOptionalAsset(assetDirectory, record.Name)
		if err != nil {
			return errors.Join(ErrRestorationFailed, err)
		}
		if exists && hash != record.InstalledHash &&
			(!record.PreviousExists || hash != record.PreviousHash) {
			return ErrExternalDrift
		}
		if !exists && record.PreviousExists {
			return ErrExternalDrift
		}
		if record.PreviousExists {
			if exists && hash == record.PreviousHash {
				if err := verifyPreviousMetadata(assetDirectory, record); err != nil {
					return errors.Join(ErrExternalDrift, err)
				}
				continue
			}
			if err := installer.restoreOne(assetDirectory, pendingDirectory, record); err != nil {
				return errors.Join(ErrRestorationFailed, err)
			}
			continue
		}
		if exists {
			if hash != record.InstalledHash {
				return ErrExternalDrift
			}
			if err := assetDirectory.Remove(record.Name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.Join(ErrRestorationFailed, err)
			}
			if err := assetDirectory.Sync(); err != nil {
				return errors.Join(ErrRestorationFailed, err)
			}
		}
	}
	if err := assetDirectory.Sync(); err != nil {
		return errors.Join(ErrRestorationFailed, err)
	}
	return installer.removePendingDirectory(assetDirectory, pendingDirectory, ErrRestorationFailed)
}

// CleanupOrphansWhileLocked completes cleanup-only recovery after the durable
// manifest-absence marker. It refuses to touch a present manifest.
func (installer *Installer) CleanupOrphansWhileLocked(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	assetDirectory, err := installer.files.OpenDir(installer.assetDir)
	if err != nil {
		return fmt.Errorf("%w: open configured asset directory", ErrInstallation)
	}
	defer assetDirectory.Close()
	pendingDirectory, err := installer.openPendingDirectory(assetDirectory, false, true)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := pendingDirectory.Lstat(manifestFileName); err == nil {
		_ = pendingDirectory.Close()
		return ErrPendingManifest
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = pendingDirectory.Close()
		return ErrInvalidManifest
	}
	entries, err := pendingDirectory.ReadDir()
	if err != nil {
		_ = pendingDirectory.Close()
		return ErrUnsafeFilesystem
	}
	return cleanupOrphanEvidence(assetDirectory, pendingDirectory, entries, ErrInstallation)
}

// Commit verifies the installed standard assets and removes transaction
// evidence in an order that permits safe retries after interruption.
func (installer *Installer) Commit(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	return installer.CommitWhileLocked(ctx)
}

// CommitWhileLocked is Commit for callers already holding the coordinator.
func (installer *Installer) CommitWhileLocked(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	assetDirectory, err := installer.files.OpenDir(installer.assetDir)
	if err != nil {
		return fmt.Errorf("%w: open configured asset directory", ErrInstallation)
	}
	defer assetDirectory.Close()
	pendingDirectory, err := installer.openPendingDirectory(assetDirectory, false, true)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := pendingDirectory.ReadDir()
	if err != nil {
		_ = pendingDirectory.Close()
		return ErrUnsafeFilesystem
	}
	manifestInfo, manifestErr := pendingDirectory.Lstat(manifestFileName)
	if errors.Is(manifestErr, os.ErrNotExist) {
		return cleanupOrphanEvidence(assetDirectory, pendingDirectory, entries, ErrInstallation)
	}
	if manifestErr != nil || manifestInfo == nil {
		_ = pendingDirectory.Close()
		return ErrInvalidManifest
	}
	manifest, err := installer.readManifest(pendingDirectory)
	if err != nil {
		_ = pendingDirectory.Close()
		return err
	}
	if err := installer.verifyLiveAssets(assetDirectory, manifest, false); err != nil {
		_ = pendingDirectory.Close()
		return err
	}
	return installer.removePendingDirectory(assetDirectory, pendingDirectory, ErrInstallation)
}

// VerifyWhileLocked verifies both live standard assets against the active
// durable install manifest without changing transaction state.
func (installer *Installer) VerifyWhileLocked(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	manifest, err := installer.readPendingManifest()
	if err != nil {
		return err
	}
	if manifest == nil {
		return ErrRecoveryRequired
	}
	assetDirectory, err := installer.files.OpenDir(installer.assetDir)
	if err != nil {
		return fmt.Errorf("%w: open configured asset directory", ErrInstallation)
	}
	defer assetDirectory.Close()
	if err := installer.verifyLiveAssets(assetDirectory, *manifest, false); err != nil {
		return err
	}
	return nil
}

func (installer *Installer) removePendingDirectory(assetDirectory, pendingDirectory Directory, operation error) error {
	if err := pendingDirectory.Remove(manifestFileName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(operation, err)
	}
	if err := pendingDirectory.Sync(); err != nil {
		_ = pendingDirectory.Close()
		return errors.Join(operation, err)
	}
	for _, name := range []string{backupName(geositeName), backupName(geoipName)} {
		if err := removeIfPresent(pendingDirectory, name); err != nil {
			return errors.Join(operation, err)
		}
	}
	if err := pendingDirectory.Sync(); err != nil {
		_ = pendingDirectory.Close()
		return errors.Join(operation, err)
	}
	if err := pendingDirectory.Close(); err != nil {
		return errors.Join(operation, err)
	}
	if err := assetDirectory.RemoveDir(pendingDirectoryName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(operation, err)
	}
	if err := assetDirectory.Sync(); err != nil {
		return errors.Join(operation, err)
	}
	return nil
}

func cleanupOrphanEvidence(assetDirectory, pendingDirectory Directory, entries []string, operation error) error {
	for _, entry := range entries {
		if entry != backupName(geositeName) && entry != backupName(geoipName) {
			_ = pendingDirectory.Close()
			return errors.Join(ErrRecoveryRequired, operation)
		}
		info, err := pendingDirectory.Lstat(entry)
		if err != nil || info == nil || !info.Mode().IsRegular() || mode(info) != uint32(privateFileMode) {
			_ = pendingDirectory.Close()
			return errors.Join(ErrRecoveryRequired, operation, ErrUnsafeFilesystem)
		}
		uid, _, err := owner(info)
		if err != nil || uid != os.Geteuid() {
			_ = pendingDirectory.Close()
			return errors.Join(ErrRecoveryRequired, operation, ErrUnsafeFilesystem)
		}
	}
	// Re-sync the manifest-absent state before treating leftover backups as
	// cleanup-only evidence. This also makes retry safe after an earlier fsync
	// failure whose durability outcome was uncertain.
	if err := pendingDirectory.Sync(); err != nil {
		_ = pendingDirectory.Close()
		return errors.Join(operation, err)
	}
	for _, entry := range entries {
		if err := removeIfPresent(pendingDirectory, entry); err != nil {
			_ = pendingDirectory.Close()
			return errors.Join(operation, err)
		}
	}
	if err := pendingDirectory.Sync(); err != nil {
		_ = pendingDirectory.Close()
		return errors.Join(operation, err)
	}
	if err := pendingDirectory.Close(); err != nil {
		return errors.Join(operation, err)
	}
	if err := assetDirectory.RemoveDir(pendingDirectoryName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(operation, err)
	}
	if err := assetDirectory.Sync(); err != nil {
		return errors.Join(operation, err)
	}
	return nil
}

func (installer *Installer) readPendingManifest() (*Manifest, error) {
	assetDirectory, err := installer.files.OpenDir(installer.assetDir)
	if err != nil {
		return nil, fmt.Errorf("%w: open configured asset directory", ErrInstallation)
	}
	defer assetDirectory.Close()
	pendingDirectory, err := installer.openPendingDirectory(assetDirectory, false, true)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer pendingDirectory.Close()
	info, err := pendingDirectory.Lstat(manifestFileName)
	if errors.Is(err, os.ErrNotExist) {
		entries, readErr := pendingDirectory.ReadDir()
		if readErr != nil {
			return nil, ErrUnsafeFilesystem
		}
		if len(entries) != 0 {
			return nil, ErrRecoveryRequired
		}
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || mode(info) != uint32(privateFileMode) {
		return nil, ErrInvalidManifest
	}
	uid, _, err := owner(info)
	if err != nil || uid != os.Geteuid() {
		return nil, ErrInvalidManifest
	}
	file, err := pendingDirectory.OpenFile(manifestFileName, os.O_RDONLY, 0)
	if err != nil {
		return nil, ErrInvalidManifest
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !fileIdentity(info, openedInfo) {
		return nil, ErrInvalidManifest
	}
	data, err := io.ReadAll(io.LimitReader(file, maxManifestSize+1))
	if err != nil || len(data) > maxManifestSize {
		return nil, ErrInvalidManifest
	}
	manifest, err := decodeManifest(data)
	if err != nil {
		return nil, err
	}
	if err := installer.verifyBackups(pendingDirectory, manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (installer *Installer) readManifest(directory Directory) (Manifest, error) {
	info, err := directory.Lstat(manifestFileName)
	if err != nil || info == nil || !info.Mode().IsRegular() || mode(info) != uint32(privateFileMode) {
		return Manifest{}, ErrInvalidManifest
	}
	uid, _, err := owner(info)
	if err != nil || uid != os.Geteuid() {
		return Manifest{}, ErrInvalidManifest
	}
	file, err := directory.OpenFile(manifestFileName, os.O_RDONLY, 0)
	if err != nil {
		return Manifest{}, ErrInvalidManifest
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !fileIdentity(info, openedInfo) {
		return Manifest{}, ErrInvalidManifest
	}
	data, err := io.ReadAll(io.LimitReader(file, maxManifestSize+1))
	if err != nil || len(data) > maxManifestSize {
		return Manifest{}, ErrInvalidManifest
	}
	manifest, err := decodeManifest(data)
	if err != nil {
		return Manifest{}, err
	}
	if err := installer.verifyBackups(directory, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (installer *Installer) openPendingDirectory(assetDirectory Directory, create, allowActive bool) (Directory, error) {
	info, err := assetDirectory.Lstat(pendingDirectoryName)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil, os.ErrNotExist
		}
		if err := assetDirectory.Mkdir(pendingDirectoryName, privateDirMode); err != nil {
			return nil, fmt.Errorf("%w: create private pending directory", ErrInstallation)
		}
		created = true
		if err := assetDirectory.Sync(); err != nil {
			return nil, fmt.Errorf("%w: sync private pending directory", ErrInstallation)
		}
		info, err = assetDirectory.Lstat(pendingDirectoryName)
	}
	if err != nil || info == nil || !info.IsDir() {
		return nil, ErrUnsafeFilesystem
	}
	directory, err := assetDirectory.OpenDir(pendingDirectoryName)
	if err != nil {
		return nil, ErrUnsafeFilesystem
	}
	if created {
		if err := directory.Chmod(privateDirMode); err != nil {
			_ = directory.Close()
			return nil, fmt.Errorf("%w: secure private pending directory", ErrInstallation)
		}
	}
	dirInfo, err := directory.Stat()
	if err != nil || dirInfo == nil {
		_ = directory.Close()
		return nil, ErrUnsafeFilesystem
	}
	uid, _, ownerErr := owner(dirInfo)
	if ownerErr != nil || uid != os.Geteuid() ||
		mode(dirInfo) != uint32(privateDirMode) {
		_ = directory.Close()
		return nil, ErrUnsafeFilesystem
	}
	entries, err := directory.ReadDir()
	if err != nil {
		_ = directory.Close()
		return nil, ErrUnsafeFilesystem
	}
	sort.Strings(entries)
	for _, entry := range entries {
		if entry == manifestFileName {
			if !allowActive {
				_ = directory.Close()
				return nil, ErrPendingManifest
			}
			continue
		}
		if allowActive && (entry == backupName(geositeName) || entry == backupName(geoipName)) {
			continue
		}
	}
	if !allowActive && len(entries) != 0 {
		_ = directory.Close()
		return nil, ErrRecoveryRequired
	}
	if allowActive {
		for _, entry := range entries {
			if entry != manifestFileName && entry != backupName(geositeName) && entry != backupName(geoipName) {
				_ = directory.Close()
				return nil, ErrRecoveryRequired
			}
		}
	}
	return directory, nil
}

func (installer *Installer) rejectSourceAliases(path string, sourceInfo os.FileInfo, assetDirectory Directory, assetNames [2]string) error {
	clean := filepath.Clean(path)
	if within(installer.assetDir, clean) || clean == filepath.Join(installer.assetDir, pendingDirectoryName) {
		return ErrInvalidInput
	}
	for _, name := range assetNames {
		info, err := assetDirectory.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info == nil || !info.Mode().IsRegular() {
			return ErrUnsafeFilesystem
		}
		if fileIdentity(sourceInfo, info) {
			return ErrInvalidInput
		}
	}
	return nil
}

func (installer *Installer) snapshotAndBackup(ctx context.Context, assetDirectory, pendingDirectory Directory, name string, record AssetRecord) (AssetRecord, bool, error) {
	info, err := assetDirectory.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		record.PreviousExists = false
		record.PreviousHash = ""
		record.BackupName = ""
		return record, false, nil
	}
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return record, false, ErrUnsafeFilesystem
	}
	file, err := assetDirectory.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return record, false, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !fileIdentity(info, openedInfo) {
		return record, false, ErrUnsafeFilesystem
	}
	if openedInfo.Size() > int64(installer.maxAssetSize) {
		return record, false, &geodatalimits.Exceeded{LimitBytes: installer.maxAssetSize}
	}
	uid, gid, err := owner(openedInfo)
	if err != nil {
		return record, false, err
	}
	backup, err := pendingDirectory.OpenFile(backupName(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return record, false, err
	}
	if err := backup.Chmod(privateFileMode); err != nil {
		_ = backup.Close()
		return record, true, err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(backup, hash), io.LimitReader(contextReader{ctx: ctx, reader: file}, int64(installer.maxAssetSize)+1))
	if copyErr == nil && n > int64(installer.maxAssetSize) {
		copyErr = ErrInvalidInput
	}
	if copyErr == nil {
		copyErr = backup.Sync()
	}
	closeErr := backup.Close()
	if copyErr != nil || closeErr != nil {
		return record, true, errors.Join(copyErr, closeErr)
	}
	current, err := assetDirectory.Lstat(name)
	if err != nil || !fileIdentity(openedInfo, current) {
		return record, true, ErrUnsafeFilesystem
	}
	record.PreviousExists = true
	record.PreviousHash = hex.EncodeToString(hash.Sum(nil))
	record.BackupName = backupName(name)
	record.PreviousMode = mode(openedInfo)
	record.PreviousUID = uid
	record.PreviousGID = gid
	return record, true, nil
}

func rejectLiveAssetAliases(directory Directory, names [2]string) error {
	var first os.FileInfo
	for _, name := range names {
		info, err := directory.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info == nil || !info.Mode().IsRegular() {
			return ErrUnsafeFilesystem
		}
		if first != nil && fileIdentity(first, info) {
			return ErrUnsafeFilesystem
		}
		first = info
	}
	return nil
}

func (installer *Installer) writeCandidate(ctx context.Context, directory Directory, name string, source File, sourceInfo os.FileInfo) (bool, string, error) {
	if sourceInfo.Size() > int64(installer.maxAssetSize) {
		return false, "", &geodatalimits.Exceeded{LimitBytes: installer.maxAssetSize}
	}
	candidate, err := directory.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return false, "", err
	}
	if err := candidate.Chmod(privateFileMode); err != nil {
		_ = candidate.Close()
		return true, "", err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(candidate, hash), io.LimitReader(contextReader{ctx: ctx, reader: source}, int64(installer.maxAssetSize)+1))
	if copyErr == nil && (n == 0 || n > int64(installer.maxAssetSize)) {
		copyErr = ErrInvalidInput
		if n > int64(installer.maxAssetSize) {
			copyErr = &geodatalimits.Exceeded{LimitBytes: installer.maxAssetSize}
		}
	}
	if copyErr == nil {
		if currentInfo, statErr := source.Stat(); statErr != nil ||
			!fileIdentity(sourceInfo, currentInfo) || currentInfo.Size() != n {
			copyErr = ErrUnsafeFilesystem
		}
	}
	if copyErr == nil {
		copyErr = candidate.Sync()
	}
	closeErr := candidate.Close()
	if copyErr != nil || closeErr != nil {
		return true, "", errors.Join(copyErr, closeErr)
	}
	return true, hex.EncodeToString(hash.Sum(nil)), nil
}

func writeManifest(directory Directory, manifest Manifest, candidateName string) (bool, error) {
	data, err := json.Marshal(manifest)
	if err != nil || len(data) > maxManifestSize {
		return false, ErrInvalidManifest
	}
	file, err := directory.OpenFile(candidateName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return false, err
	}
	if err := file.Chmod(privateFileMode); err != nil {
		closeErr := file.Close()
		cleanupErr := removeIfPresent(directory, candidateName)
		return false, errors.Join(err, closeErr, cleanupErr)
	}
	if _, err = io.Copy(file, bytes.NewReader(data)); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		cleanupErr := removeIfPresent(directory, candidateName)
		return false, errors.Join(err, closeErr, cleanupErr)
	}
	if err := directory.Rename(candidateName, manifestFileName); err != nil {
		cleanupErr := removeIfPresent(directory, candidateName)
		return false, errors.Join(err, cleanupErr)
	}
	return true, nil
}

func applyInstallPolicy(directory Directory, candidateName string, record AssetRecord) error {
	file, err := directory.OpenFile(candidateName, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	uid, gid := os.Geteuid(), os.Getegid()
	wantedMode := os.FileMode(installedMode)
	if record.PreviousExists {
		uid, gid = record.PreviousUID, record.PreviousGID
		wantedMode = modeValue(record.PreviousMode)
	}
	if err := setOwnership(file, uid, gid); err != nil {
		return err
	}
	if err := file.Chmod(wantedMode); err != nil {
		return err
	}
	return file.Sync()
}

func (installer *Installer) restoreAssets(assetDirectory, pendingDirectory Directory, records [2]AssetRecord) error {
	var failures []error
	for index := len(records) - 1; index >= 0; index-- {
		record := records[index]
		if record.PreviousExists {
			if err := installer.restoreOne(assetDirectory, pendingDirectory, record); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		err := assetDirectory.Remove(record.Name)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, err)
			continue
		}
		if err := assetDirectory.Sync(); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (installer *Installer) restoreOne(assetDirectory, pendingDirectory Directory, record AssetRecord) error {
	backup, err := pendingDirectory.OpenFile(record.BackupName, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer backup.Close()
	backupInfo, err := backup.Stat()
	if err != nil || !backupInfo.Mode().IsRegular() || mode(backupInfo) != uint32(privateFileMode) {
		return ErrUnsafeFilesystem
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	temporaryName := "." + record.Name + ".xch-restore-" + id
	temporary, err := assetDirectory.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(backup, int64(installer.maxAssetSize)+1))
	if copyErr == nil && n > int64(installer.maxAssetSize) {
		copyErr = ErrInvalidManifest
	}
	if copyErr == nil && hex.EncodeToString(hash.Sum(nil)) != record.PreviousHash {
		copyErr = ErrInvalidManifest
	}
	if copyErr == nil {
		copyErr = setOwnership(temporary, record.PreviousUID, record.PreviousGID)
	}
	if copyErr == nil {
		copyErr = temporary.Chmod(modeValue(record.PreviousMode))
	}
	if copyErr == nil {
		copyErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil {
		_ = assetDirectory.Remove(temporaryName)
		return errors.Join(copyErr, closeErr)
	}
	if err := assetDirectory.Rename(temporaryName, record.Name); err != nil {
		_ = assetDirectory.Remove(temporaryName)
		return err
	}
	return assetDirectory.Sync()
}

func setOwnership(file File, uid, gid int) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	currentUID, currentGID, err := owner(info)
	if err != nil {
		return err
	}
	if currentUID == uid && currentGID == gid {
		return nil
	}
	return file.Chown(uid, gid)
}

func cleanupStagedFiles(assetDirectory, pendingDirectory Directory, staged [2]stagedAsset, records [2]AssetRecord, manifestMade bool, manifestCandidate string) error {
	var failures []error
	for _, candidate := range staged {
		if candidate.candidateMade {
			if err := removeIfPresent(assetDirectory, candidate.candidate); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if err := assetDirectory.Sync(); err != nil {
		failures = append(failures, err)
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}

	if err := removeIfPresent(pendingDirectory, manifestCandidate); err != nil {
		return err
	}
	if manifestMade {
		if err := removeIfPresent(pendingDirectory, manifestFileName); err != nil {
			return err
		}
		if err := pendingDirectory.Sync(); err != nil {
			return err
		}
	}
	for index, record := range records {
		if staged[index].backupMade {
			if err := removeIfPresent(pendingDirectory, backupName(record.Name)); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if err := pendingDirectory.Sync(); err != nil {
		failures = append(failures, err)
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	if err := pendingDirectory.Close(); err != nil {
		return err
	}
	if err := assetDirectory.RemoveDir(pendingDirectoryName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return assetDirectory.Sync()
}

func cleanupCandidates(assetDirectory Directory, staged [2]stagedAsset) error {
	var failures []error
	for _, candidate := range staged {
		if candidate.candidateMade {
			if err := removeIfPresent(assetDirectory, candidate.candidate); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if err := assetDirectory.Sync(); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func removeIfPresent(directory Directory, name string) error {
	err := directory.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func decodeManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	if len(data) == 0 || len(data) > maxManifestSize || !utf8.Valid(data) {
		return manifest, ErrInvalidManifest
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return manifest, ErrInvalidManifest
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(data, &shape); err != nil ||
		!exactKeys(shape, "version", "id", "assets") {
		return Manifest{}, ErrInvalidManifest
	}
	var assetShapes []map[string]json.RawMessage
	if err := json.Unmarshal(shape["assets"], &assetShapes); err != nil || len(assetShapes) != 2 {
		return Manifest{}, ErrInvalidManifest
	}
	requiredAssetKeys := []string{
		"name", "installedHash", "previousExists", "previousHash",
		"backupName", "previousMode", "previousUID", "previousGID",
	}
	for _, assetShape := range assetShapes {
		if !exactKeys(assetShape, requiredAssetKeys...) {
			return Manifest{}, ErrInvalidManifest
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, ErrInvalidManifest
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, ErrInvalidManifest
	}
	return manifest, nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalidManifest
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return ErrInvalidManifest
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrInvalidManifest
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrInvalidManifest
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return ErrInvalidManifest
	}
}

func exactKeys(object map[string]json.RawMessage, required ...string) bool {
	if len(object) != len(required) {
		return false
	}
	for _, key := range required {
		if _, exists := object[key]; !exists {
			return false
		}
	}
	return true
}

func (installer *Installer) verifyBackups(directory Directory, manifest Manifest) error {
	expected := make(map[string]struct{}, 2)
	for _, record := range manifest.Assets {
		if !record.PreviousExists {
			continue
		}
		expected[record.BackupName] = struct{}{}
		info, err := directory.Lstat(record.BackupName)
		if err != nil || info == nil || !info.Mode().IsRegular() || mode(info) != uint32(privateFileMode) {
			return ErrInvalidManifest
		}
		uid, _, err := owner(info)
		if err != nil || uid != os.Geteuid() {
			return ErrInvalidManifest
		}
		file, err := directory.OpenFile(record.BackupName, os.O_RDONLY, 0)
		if err != nil {
			return ErrInvalidManifest
		}
		openedInfo, statErr := file.Stat()
		hash := sha256.New()
		n, readErr := io.Copy(hash, io.LimitReader(file, int64(installer.maxAssetSize)+1))
		closeErr := file.Close()
		if statErr != nil || !fileIdentity(info, openedInfo) ||
			readErr != nil || closeErr != nil || n > int64(installer.maxAssetSize) ||
			hex.EncodeToString(hash.Sum(nil)) != record.PreviousHash {
			return ErrInvalidManifest
		}
	}
	entries, err := directory.ReadDir()
	if err != nil {
		return ErrUnsafeFilesystem
	}
	for _, entry := range entries {
		if entry == manifestFileName {
			continue
		}
		if _, ok := expected[entry]; !ok {
			return ErrInvalidManifest
		}
		delete(expected, entry)
	}
	if len(expected) != 0 {
		return ErrInvalidManifest
	}
	return nil
}

func (installer *Installer) verifyLiveAssets(directory Directory, manifest Manifest, rollback bool) error {
	for _, record := range manifest.Assets {
		hash, exists, err := installer.hashOptionalAsset(directory, record.Name)
		if err != nil {
			return err
		}
		if !rollback {
			if !exists || hash != record.InstalledHash {
				return ErrExternalDrift
			}
			continue
		}
		if exists && hash != record.InstalledHash &&
			(!record.PreviousExists || hash != record.PreviousHash) {
			return ErrExternalDrift
		}
		if rollback && exists && record.PreviousExists && hash == record.PreviousHash {
			if err := verifyPreviousMetadata(directory, record); err != nil {
				return errors.Join(ErrExternalDrift, err)
			}
		}
		if !exists && record.PreviousExists {
			return ErrExternalDrift
		}
	}
	return nil
}

func verifyPreviousMetadata(directory Directory, record AssetRecord) error {
	info, err := directory.Lstat(record.Name)
	if err != nil || info == nil || !info.Mode().IsRegular() || mode(info) != record.PreviousMode {
		return ErrExternalDrift
	}
	uid, gid, err := owner(info)
	if err != nil || uid != record.PreviousUID || gid != record.PreviousGID {
		return ErrExternalDrift
	}
	return nil
}

func (installer *Installer) hashOptionalAsset(directory Directory, name string) (string, bool, error) {
	return installer.hashOptionalAssetContext(context.Background(), directory, name)
}

func (installer *Installer) hashOptionalAssetContext(ctx context.Context, directory Directory, name string) (string, bool, error) {
	info, err := directory.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return "", false, ErrUnsafeFilesystem
	}
	file, err := directory.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return "", false, ErrUnsafeFilesystem
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !fileIdentity(info, openedInfo) {
		return "", false, ErrUnsafeFilesystem
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(contextReader{ctx: ctx, reader: file}, int64(installer.maxAssetSize)+1))
	if err != nil || n > int64(installer.maxAssetSize) {
		return "", false, ErrUnsafeFilesystem
	}
	return hex.EncodeToString(hash.Sum(nil)), true, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.Version != manifestVersion || !validID(manifest.ID) {
		return ErrInvalidManifest
	}
	for index, expectedName := range [2]string{geositeName, geoipName} {
		record := manifest.Assets[index]
		if record.Name != expectedName || !validHash(record.InstalledHash) {
			return ErrInvalidManifest
		}
		if record.PreviousExists {
			if !validHash(record.PreviousHash) || record.BackupName != backupName(expectedName) ||
				record.PreviousUID < 0 || record.PreviousGID < 0 ||
				uint64(record.PreviousUID) > uint64(^uint32(0)) ||
				uint64(record.PreviousGID) > uint64(^uint32(0)) ||
				record.PreviousMode&^uint32(07777) != 0 {
				return ErrInvalidManifest
			}
		} else if record.PreviousHash != "" || record.BackupName != "" ||
			record.PreviousMode != 0 || record.PreviousUID != 0 || record.PreviousGID != 0 {
			return ErrInvalidManifest
		}
	}
	return nil
}

func validID(value string) bool {
	if len(value) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value && len(decoded) == 16
}

func validSourcePath(path, assetDir string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path &&
		filepath.Base(path) != "." && filepath.Base(path) != string(filepath.Separator) &&
		!within(assetDir, path)
}

func backupName(asset string) string {
	return asset + ".previous"
}

func validHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value && len(decoded) == sha256.Size
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func closeFiles(files []File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
