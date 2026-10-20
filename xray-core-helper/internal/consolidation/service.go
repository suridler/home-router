// Package consolidation prepares a durable custom geosite/geoip pair while
// proxy-domain mutations are frozen.
package consolidation

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
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/domain"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/geodatainstall"
	"home-router/xray-core-helper/internal/geodatalimits"
	"home-router/xray-core-helper/internal/usergeoip"
)

const (
	pendingName       = ".xray-core-helper-consolidation-pending"
	manifestName      = "manifest.json"
	manifestCandidate = ".manifest.json.candidate"
	inputName         = "userlist"
	emptyDomain       = "xray-core-helper-empty-domain.invalid"
	maxManifestSize   = 2 << 20
	maxDomains        = 4096
	privateMode       = 0700
	privateFileMode   = 0600
	installedMode     = 0644
	manifestVersion   = 3
)

var (
	ErrInvalidInput   = errors.New("invalid consolidation input")
	ErrPending        = errors.New("consolidation recovery is pending")
	ErrBuilder        = errors.New("geosite builder failed")
	ErrBuilderOutput  = errors.New("geosite builder output is invalid")
	ErrValidation     = errors.New("custom geodata validation failed")
	ErrFilesystem     = errors.New("consolidation filesystem operation failed")
	ErrManifest       = errors.New("consolidation manifest is invalid")
	ErrExternalDrift  = errors.New("custom geodata changed outside consolidation")
	ErrRollback       = errors.New("consolidation rollback is incomplete")
	ErrReservedDomain = errors.New("desired domains contain a reserved consolidation sentinel")
)

type Store interface {
	BeginConsolidation(context.Context) (domainstore.Snapshot, error)
	FinalizeConsolidation(context.Context, []string) error
	SetConsolidationActive(context.Context, bool) error
	Snapshot(context.Context) (domainstore.Snapshot, error)
}

func (service *Service) removeFailedCandidate(directory geodatainstall.Directory) error {
	err := directory.Remove(service.liveCandidateName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil {
		if syncErr := directory.Sync(); syncErr != nil {
			return errors.Join(errRenameAttempted, syncErr)
		}
		return nil
	}
	return errors.Join(errRenameAttempted, err)
}

func (service *Service) removeInterruptedCandidate(directory geodatainstall.Directory, manifest Manifest) error {
	info, err := directory.Lstat(service.liveCandidateName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return ErrExternalDrift
	}
	uid, _, err := fileOwner(info)
	if err != nil || uid != os.Geteuid() {
		return ErrExternalDrift
	}
	_, hash, hashErr := service.readBoundedRegular(directory, service.liveCandidateName)
	if hashErr != nil && mode(info) != privateFileMode {
		return ErrExternalDrift
	}
	if hashErr == nil && hash != manifest.CandidateHash &&
		(!manifest.PreviousExists || hash != manifest.PreviousHash) && mode(info) != privateFileMode {
		return ErrExternalDrift
	}
	if err := directory.Remove(service.liveCandidateName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(ErrRollback, err)
	}
	if err := directory.Sync(); err != nil {
		return errors.Join(ErrRollback, err)
	}
	return nil
}

type Runner interface {
	Run(context.Context, string, []string) error
}

type Validator interface {
	ValidateCustomAssets(context.Context, string, string, string, string) error
}

type Options struct {
	MaxAssetSizeMB int
	Files          geodatainstall.FileSystem
	Runner         Runner
	GeositeName    string
	GeoipName      string
}

type Service struct {
	maxAssetSize      int
	store             Store
	assetDir          string
	builder           string
	files             geodatainstall.FileSystem
	runner            Runner
	validator         Validator
	geoipName         string
	candidateName     string
	backupName        string
	liveCandidateName string
}

type Manifest struct {
	Geoip          geodatainstall.AssetRecord `json:"geoip"`
	Version        int                        `json:"version"`
	ID             string                     `json:"id"`
	Domains        []string                   `json:"domains"`
	GeositeName    string                     `json:"geositeName"`
	CandidateHash  string                     `json:"candidateHash"`
	PreviousExists bool                       `json:"previousExists"`
	PreviousHash   string                     `json:"previousHash"`
	PreviousMode   uint32                     `json:"previousMode"`
	PreviousUID    int                        `json:"previousUID"`
	PreviousGID    int                        `json:"previousGID"`
}

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, executable string, args []string) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = []string{}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func New(store Store, assetDir, builder string, validator Validator, options Options) (*Service, error) {
	if store == nil || validator == nil ||
		(assetDir != "/usr/local/share/xray" && assetDir != "/var/lib/xray-core-helper/geodata") ||
		!filepath.IsAbs(builder) || filepath.Clean(builder) != builder {
		return nil, ErrInvalidInput
	}
	geositeName, backupName, liveCandidateName, err := geositeNames(options.GeositeName)
	if err != nil {
		return nil, err
	}
	if options.GeoipName == "" {
		options.GeoipName = bootstrap.DefaultUserGeoipName
	}
	if !bootstrap.ValidUserGeoipName(options.GeoipName) {
		return nil, ErrInvalidInput
	}
	if options.Files == nil {
		options.Files = consolidationOSFileSystem{}
	}
	if options.Runner == nil {
		options.Runner = commandRunner{}
	}
	return &Service{
		store: store, assetDir: assetDir, builder: builder,
		files: options.Files, runner: options.Runner, validator: validator,
		candidateName: geositeName, geoipName: options.GeoipName,
		maxAssetSize: geodatalimits.Bytes(options.MaxAssetSizeMB),
		backupName:   backupName, liveCandidateName: liveCandidateName,
	}, nil
}

// NewRecoveryService constructs only the filesystem/domain boundaries needed
// for inspecting and resolving existing evidence; it does not need a builder
// or candidate validator.
func NewRecoveryService(store Store, assetDir, geositeName, geoipName string, files geodatainstall.FileSystem, limitMB ...int) (*Service, error) {
	if store == nil ||
		(assetDir != "/usr/local/share/xray" && assetDir != "/var/lib/xray-core-helper/geodata") {
		return nil, ErrInvalidInput
	}
	resolvedName, backupName, liveCandidateName, err := geositeNames(geositeName)
	if err != nil {
		return nil, err
	}
	if geoipName == "" {
		geoipName = bootstrap.DefaultUserGeoipName
	}
	if !bootstrap.ValidUserGeoipName(geoipName) {
		return nil, ErrInvalidInput
	}
	if files == nil {
		files = consolidationOSFileSystem{}
	}
	return &Service{
		store: store, assetDir: assetDir, files: files,
		candidateName: resolvedName, geoipName: geoipName,
		maxAssetSize: geodatalimits.Bytes(firstLimitMB(limitMB)),
		backupName:   backupName, liveCandidateName: liveCandidateName,
	}, nil
}

func geositeNames(name string) (string, string, string, error) {
	if name == "" {
		name = bootstrap.DefaultUserGeositeName
	}
	if !bootstrap.ValidUserGeositeName(name) {
		return "", "", "", ErrInvalidInput
	}
	return name, name + ".previous", "." + name + ".xch-candidate", nil
}

type InspectionPhase uint8

const (
	InspectionClean InspectionPhase = iota
	InspectionPartial
	InspectionManifest
	InspectionInvalid
)

type LiveGeositePhase uint8

const (
	LiveGeositeAbsent LiveGeositePhase = iota
	LiveGeositeCandidate
	LiveGeositePrevious
	LiveGeositeDrift
	LiveAssetsMixed
)

type Inspection struct {
	Phase             InspectionPhase
	Manifest          *Manifest
	PreparedEvidence  bool
	FinalizedEvidence bool
	Live              LiveGeositePhase
	TransientPresent  bool
}

// Inspect validates the custom-consolidation evidence and live asset pair using
// the same strict parsers, artifact checks and no-follow filesystem boundary
// used by the mutating operations.
func (service *Service) Inspect(ctx context.Context) (Inspection, error) {
	if ctx == nil {
		return Inspection{}, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	assetDirectory, err := service.files.OpenDir(service.assetDir)
	if err != nil {
		return Inspection{}, fmt.Errorf("%w: open asset directory", ErrFilesystem)
	}
	defer assetDirectory.Close()

	inspection := Inspection{}
	for _, transientName := range []string{service.liveCandidateName, "." + service.geoipName + ".xch-candidate"} {
		if info, statErr := assetDirectory.Lstat(transientName); statErr == nil {
			if info == nil || !info.Mode().IsRegular() {
				return Inspection{Phase: InspectionInvalid}, nil
			}
			uid, _, ownerErr := fileOwner(info)
			if ownerErr != nil || uid != os.Geteuid() {
				return Inspection{Phase: InspectionInvalid}, nil
			}
			if _, _, readErr := service.readBoundedRegular(assetDirectory, transientName); readErr != nil {
				return Inspection{Phase: InspectionInvalid}, nil
			}
			inspection.TransientPresent = true
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return Inspection{Phase: InspectionInvalid}, nil
		}

	}

	stageDirectory, err := assetDirectory.OpenDir(pendingName)
	if errors.Is(err, os.ErrNotExist) {
		inspection.Phase = InspectionClean
	} else if err != nil {
		return Inspection{Phase: InspectionInvalid}, nil
	} else {
		defer stageDirectory.Close()
		stageInfo, statErr := stageDirectory.Stat()
		if statErr != nil || stageInfo == nil || !stageInfo.IsDir() ||
			mode(stageInfo) != privateMode {
			return Inspection{Phase: InspectionInvalid}, nil
		}
		uid, _, ownerErr := fileOwner(stageInfo)
		if ownerErr != nil || uid != os.Geteuid() {
			return Inspection{Phase: InspectionInvalid}, nil
		}
		entries, readErr := stageDirectory.ReadDir()
		if readErr != nil {
			return Inspection{Phase: InspectionInvalid}, nil
		}
		for _, entry := range entries {
			if !service.allowedStageEntry(entry) {
				return Inspection{Phase: InspectionInvalid}, nil
			}
		}
		manifestInfo, statErr := stageDirectory.Lstat(manifestName)
		if errors.Is(statErr, os.ErrNotExist) {
			if service.validatePartialStageArtifacts(stageDirectory) {
				inspection.Phase = InspectionPartial
			} else {
				return Inspection{Phase: InspectionInvalid}, nil
			}
		} else if statErr != nil || manifestInfo == nil {
			return Inspection{Phase: InspectionInvalid}, nil
		} else {
			manifest, manifestErr := service.readManifestMetadata(stageDirectory)
			if manifestErr != nil {
				return Inspection{Phase: InspectionInvalid}, nil
			}
			inspection.Phase = InspectionManifest
			inspection.Manifest = &manifest
			inspection.PreparedEvidence = service.validateStageContents(stageDirectory, manifest) == nil &&
				service.validateStageArtifacts(stageDirectory, manifest, false) == nil
			inspection.FinalizedEvidence = service.validateFinalizedStageContents(stageDirectory, manifest) == nil
		}
	}

	if inspection.Manifest != nil {
		phase, err := service.pairPhase(ctx, assetDirectory, *inspection.Manifest)
		if err != nil {
			return Inspection{Phase: InspectionInvalid}, nil
		}
		inspection.Live = phase
	} else {
		// Without a manifest there is no candidate to recognize, but still
		// inspect both managed paths so unsafe entries cannot clear a freeze.
		inspection.Live = LiveGeositeAbsent
		for _, name := range []string{service.candidateName, service.geoipName} {
			_, exists, err := service.hashOptionalContext(ctx, assetDirectory, name)
			if err != nil {
				return Inspection{Phase: InspectionInvalid}, nil
			}
			if exists {
				inspection.Live = LiveGeositeDrift
			}
		}
	}

	return inspection, nil
}

func (service *Service) validatePartialStageArtifacts(directory geodatainstall.Directory) bool {
	entries, err := directory.ReadDir()
	if err != nil {
		return false
	}
	for _, name := range entries {
		if !service.allowedStageEntry(name) || name == manifestName {
			return false
		}
		info, err := directory.Lstat(name)
		if err != nil || info == nil || !info.Mode().IsRegular() {
			return false
		}
		uid, _, err := fileOwner(info)
		if err != nil || uid != os.Geteuid() {
			return false
		}
		if (name == service.backupName || name == service.geoipName+".previous") && mode(info) != privateFileMode {
			return false
		}
		if (name == inputName || name == manifestCandidate) && mode(info) != privateFileMode {
			return false
		}
		file, err := directory.OpenFile(name, os.O_RDONLY, 0)
		if err != nil {
			return false
		}
		opened, statErr := file.Stat()
		limit := maxManifestSize
		if name != manifestCandidate {
			limit = service.maxAssetSize
		}
		data, readErr := io.ReadAll(io.LimitReader(file, int64(limit)+1))
		closeErr := file.Close()
		if statErr != nil || opened == nil || !opened.Mode().IsRegular() ||
			!os.SameFile(info, opened) || readErr != nil || closeErr != nil ||
			len(data) > limit || int64(len(data)) != opened.Size() {
			return false
		}
	}
	return true
}

func (service *Service) verifyLivePrevious(directory geodatainstall.Directory, manifest Manifest) error {
	info, err := directory.Lstat(service.candidateName)
	if err != nil || info == nil || !info.Mode().IsRegular() ||
		uint32(mode(info)) != manifest.PreviousMode {
		return ErrExternalDrift
	}
	uid, gid, err := fileOwner(info)
	if err != nil || uid != manifest.PreviousUID || gid != manifest.PreviousGID {
		return ErrExternalDrift
	}
	return nil
}

// Prepare freezes and snapshots desired domains, builds and validates the
// custom list, then atomically installs it while retaining durable rollback
// evidence and the candidate for a later finalize operation.
func (service *Service) Prepare(ctx context.Context) (resultErr error) {
	if ctx == nil {
		return ErrInvalidInput
	}
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	return service.PrepareWhileLocked(ctx)
}

// PrepareWhileLocked performs Prepare when the caller owns the process
// mutation coordinator.
func (service *Service) PrepareWhileLocked(ctx context.Context) (resultErr error) {
	if ctx == nil {
		return ErrInvalidInput
	}
	snapshot, err := service.store.BeginConsolidation(ctx)
	if err != nil {
		return fmt.Errorf("freeze domain mutations: %w", err)
	}
	frozen := true
	liveReplacementStarted := false
	stageCreated := false
	defer func() {
		if resultErr == nil {
			return
		}
		if liveReplacementStarted {
			recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if rollbackErr := service.RollbackWhileLocked(recoveryContext); rollbackErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("rollback failed consolidation prepare: %w", rollbackErr))
			}
			return
		}
		if frozen {
			recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if err := service.store.SetConsolidationActive(recoveryContext, false); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("unfreeze domain mutations: %w", err))
				return
			}
			frozen = false
		}
		if stageCreated {
			if err := service.cleanStage(); err != nil {
				resultErr = errors.Join(resultErr, err)
			}
		}
	}()

	domains, err := normalizedDomains(snapshot.Desired)
	if err != nil {
		return err
	}
	assetDirectory, stageDirectory, created, err := service.createStage()
	stageCreated = created
	if assetDirectory != nil {
		defer assetDirectory.Close()
	}
	if stageDirectory != nil {
		defer stageDirectory.Close()
	}
	if err != nil {
		return err
	}
	onlyDomains, ips, err := domain.SplitResources(domains)
	if err != nil {
		return err
	}
	input := domainInput(onlyDomains)
	if err := writeFile(stageDirectory, inputName, []byte(input), privateFileMode); err != nil {
		return fmt.Errorf("%w: stage deterministic domain input", ErrFilesystem)
	}
	if err := stageDirectory.Sync(); err != nil {
		return fmt.Errorf("%w: sync domain input", ErrFilesystem)
	}
	pendingDirectory := filepath.Join(service.assetDir, pendingName)
	args := []string{
		"-datapath", pendingDirectory,
		"-outputname", service.candidateName,
		"-outputdir", pendingDirectory,
	}
	if err := service.runner.Run(ctx, service.builder, args); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrBuilder
	}

	_, hash, err := service.readBoundedRegular(stageDirectory, service.candidateName)
	if err != nil {
		return errors.Join(ErrBuilderOutput, err)
	}
	ipCandidate, err := usergeoip.Encode(ips)
	if err != nil {
		return err
	}
	if err := writeFile(stageDirectory, service.geoipName, ipCandidate, privateFileMode); err != nil {
		return errors.Join(ErrFilesystem, err)
	}
	if err := stageDirectory.Sync(); err != nil {
		return errors.Join(ErrFilesystem, err)
	}
	ipHash := hashBytes(ipCandidate)
	candidatePath := filepath.Join(service.assetDir, pendingName, service.candidateName)
	if err := service.validator.ValidateCustomAssets(ctx, candidatePath, hash, filepath.Join(service.assetDir, pendingName, service.geoipName), ipHash); err != nil {
		return errors.Join(ErrValidation, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	manifest, err := service.makeManifest(ctx, assetDirectory, stageDirectory, domains, hash)
	if err != nil {
		return err
	}
	ipService := service.ipService()
	ipManifest, err := ipService.makeManifest(ctx, assetDirectory, stageDirectory, nil, ipHash)
	if err != nil {
		return err
	}
	manifest.Geoip = ipRecord(ipManifest, ipService.backupName)
	if err := writeManifest(stageDirectory, manifest); err != nil {
		return fmt.Errorf("%w: persist prepare manifest", ErrFilesystem)
	}
	if err := stageDirectory.Sync(); err != nil {
		return fmt.Errorf("%w: sync prepare manifest", ErrFilesystem)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := service.checkPairPrevious(ctx, assetDirectory, manifest); err != nil {
		return err
	}
	views := service.assets(manifest)
	candidates := make([][]byte, len(views))
	for i, asset := range views {
		data, hash, err := asset.service.readBoundedRegular(stageDirectory, asset.service.candidateName)
		if err != nil || hash != asset.manifest.CandidateHash {
			return ErrBuilderOutput
		}
		candidates[i] = data
	}
	for i, asset := range views {
		if err := asset.service.installCandidate(assetDirectory, candidates[i], asset.manifest); err != nil {
			if errors.Is(err, errRenameAttempted) {
				liveReplacementStarted = true
			}
			return err
		}
		liveReplacementStarted = true
	}
	frozen = false
	return nil
}

// VerifyWhileLocked checks that the prepared custom asset pair and frozen
// snapshot still match their durable evidence without committing the baseline.
func (service *Service) VerifyWhileLocked(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	assetDirectory, err := service.files.OpenDir(service.assetDir)
	if err != nil {
		return fmt.Errorf("%w: open asset directory", ErrFilesystem)
	}
	defer assetDirectory.Close()
	stageDirectory, err := assetDirectory.OpenDir(pendingName)
	if err != nil {
		return ErrPending
	}
	defer stageDirectory.Close()
	stageInfo, err := stageDirectory.Stat()
	if err != nil || stageInfo == nil || !stageInfo.IsDir() || mode(stageInfo) != privateMode {
		return ErrManifest
	}
	stageUID, _, err := fileOwner(stageInfo)
	if err != nil || stageUID != os.Geteuid() {
		return ErrManifest
	}
	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return err
	}
	if !snapshot.ConsolidationActive {
		return ErrPending
	}
	manifest, err := service.readManifest(stageDirectory)
	if err != nil {
		return err
	}
	if err := service.validateStageContents(stageDirectory, manifest); err != nil {
		return err
	}
	if !equalStrings(snapshot.Desired, manifest.Domains) {
		return domainstore.ErrPreparedDomainsMismatch
	}
	if err := service.checkPairCandidates(ctx, assetDirectory, manifest); err != nil {
		return err
	}
	return nil
}

// Rollback restores only a file matching the pending candidate or the saved
// previous hash. It is safe to retry after a partial restore or flag update.
func (service *Service) Rollback(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	return service.RollbackWhileLocked(ctx)
}

// RollbackWhileLocked performs Rollback when the caller owns the process
// mutation coordinator.
func (service *Service) RollbackWhileLocked(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	assetDirectory, err := service.files.OpenDir(service.assetDir)
	if err != nil {
		return fmt.Errorf("%w: open asset directory", ErrFilesystem)
	}
	defer assetDirectory.Close()
	stageDirectory, err := assetDirectory.OpenDir(pendingName)
	if errors.Is(err, os.ErrNotExist) {
		snapshot, snapshotErr := service.store.Snapshot(ctx)
		if snapshotErr != nil {
			return snapshotErr
		}
		if snapshot.ConsolidationActive {
			return ErrPending
		}
		return nil
	}
	if err != nil {
		return ErrPending
	}
	defer stageDirectory.Close()
	stageInfo, err := stageDirectory.Stat()
	if err != nil || stageInfo == nil || !stageInfo.IsDir() || mode(stageInfo) != privateMode {
		return ErrManifest
	}
	stageUID, _, ownerErr := fileOwner(stageInfo)
	if ownerErr != nil || stageUID != os.Geteuid() {
		return ErrManifest
	}
	entries, err := stageDirectory.ReadDir()
	if err != nil {
		return ErrManifest
	}
	for _, entry := range entries {
		if !service.allowedStageEntry(entry) {
			return ErrManifest
		}
	}
	if _, err := stageDirectory.Lstat(manifestName); errors.Is(err, os.ErrNotExist) {
		snapshot, snapshotErr := service.store.Snapshot(ctx)
		if snapshotErr != nil {
			return snapshotErr
		}
		if snapshot.ConsolidationActive {
			return ErrPending
		}
		if err := stageDirectory.Close(); err != nil {
			return ErrFilesystem
		}
		return service.cleanStageDirectory(assetDirectory)
	} else if err != nil {
		return ErrManifest
	}
	manifest, err := service.readManifest(stageDirectory)
	if err != nil {
		return err
	}
	if err := service.validateStageContents(stageDirectory, manifest); err != nil {
		return err
	}
	if err := service.rollbackPair(ctx, assetDirectory, stageDirectory, manifest); err != nil {
		return err
	}

	if err := assetDirectory.Sync(); err != nil {
		return fmt.Errorf("%w: sync restored custom geodata state", ErrRollback)
	}
	if err := service.store.SetConsolidationActive(ctx, false); err != nil {
		return fmt.Errorf("%w: clear consolidation flag", ErrRollback)
	}
	if err := stageDirectory.Close(); err != nil {
		return fmt.Errorf("%w: close pending directory", ErrFilesystem)
	}
	if err := service.cleanStageDirectory(assetDirectory); err != nil {
		return err
	}
	return nil
}

// CleanupPartialWhileLocked removes only validated manifest-absent staging
// left before the live replacement phase. Active freezes and manifests belong
// to the rollback/finalization paths and are never touched here.
func (service *Service) CleanupPartialWhileLocked(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.ConsolidationActive {
		return ErrPending
	}
	assetDirectory, err := service.files.OpenDir(service.assetDir)
	if err != nil {
		return fmt.Errorf("%w: open asset directory", ErrFilesystem)
	}
	defer assetDirectory.Close()
	stage, err := assetDirectory.OpenDir(pendingName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrPending
	}
	stageInfo, err := stage.Stat()
	if err != nil || stageInfo == nil || !stageInfo.IsDir() ||
		mode(stageInfo) != privateMode {
		_ = stage.Close()
		return ErrManifest
	}
	uid, _, err := fileOwner(stageInfo)
	if err != nil || uid != os.Geteuid() {
		_ = stage.Close()
		return ErrManifest
	}
	if _, err := stage.Lstat(manifestName); err == nil {
		_ = stage.Close()
		return ErrPending
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = stage.Close()
		return ErrManifest
	}
	if !service.validatePartialStageArtifacts(stage) {
		_ = stage.Close()
		return ErrManifest
	}
	if err := stage.Close(); err != nil {
		return ErrFilesystem
	}
	return service.cleanPartialStageDirectory(assetDirectory)
}

// RecoverIncompletePrepareWhileLocked clears only a freeze/stage left before
// durable manifest creation. Without that manifest replacement cannot have
// started, so no runtime restart or live-file restoration is needed.
func (service *Service) RecoverIncompletePrepareWhileLocked(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return err
	}
	if !snapshot.ConsolidationActive {
		return ErrPending
	}
	assetDirectory, err := service.files.OpenDir(service.assetDir)
	if err != nil {
		return fmt.Errorf("%w: open asset directory", ErrFilesystem)
	}
	defer assetDirectory.Close()
	for _, name := range []string{service.liveCandidateName, "." + service.geoipName + ".xch-candidate"} {
		if _, err := assetDirectory.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return ErrPending
		}
	}
	stage, err := assetDirectory.OpenDir(pendingName)
	if errors.Is(err, os.ErrNotExist) {
		return service.store.SetConsolidationActive(ctx, false)
	}
	if err != nil {
		return ErrPending
	}
	stageInfo, err := stage.Stat()
	if err != nil || stageInfo == nil || !stageInfo.IsDir() || mode(stageInfo) != privateMode {
		_ = stage.Close()
		return ErrManifest
	}
	uid, _, err := fileOwner(stageInfo)
	if err != nil || uid != os.Geteuid() {
		_ = stage.Close()
		return ErrManifest
	}
	if _, err := stage.Lstat(manifestName); err == nil {
		_ = stage.Close()
		return ErrPending
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = stage.Close()
		return ErrManifest
	}
	if !service.validatePartialStageArtifacts(stage) {
		_ = stage.Close()
		return ErrManifest
	}
	if err := stage.Close(); err != nil {
		return ErrFilesystem
	}
	if err := service.cleanPartialStageDirectory(assetDirectory); err != nil {
		return err
	}
	return service.store.SetConsolidationActive(ctx, false)
}

func (service *Service) cleanPartialStageDirectory(assetDirectory geodatainstall.Directory) error {
	stage, err := assetDirectory.OpenDir(pendingName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrFilesystem
	}
	entries, err := stage.ReadDir()
	if err != nil || !service.validatePartialStageArtifacts(stage) {
		_ = stage.Close()
		return ErrManifest
	}
	if _, err := stage.Lstat(manifestName); err == nil {
		_ = stage.Close()
		return ErrPending
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = stage.Close()
		return ErrManifest
	}
	for _, name := range entries {
		if name == manifestName || !service.allowedStageEntry(name) {
			_ = stage.Close()
			return ErrManifest
		}
		if _, err := stage.Lstat(manifestName); err == nil {
			_ = stage.Close()
			return ErrPending
		} else if !errors.Is(err, os.ErrNotExist) {
			_ = stage.Close()
			return ErrManifest
		}
		if err := stage.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = stage.Close()
			return ErrFilesystem
		}
	}
	if _, err := stage.Lstat(manifestName); err == nil {
		_ = stage.Close()
		return ErrPending
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = stage.Close()
		return ErrManifest
	}
	if err := stage.Sync(); err != nil {
		_ = stage.Close()
		return ErrFilesystem
	}
	if err := stage.Close(); err != nil {
		return ErrFilesystem
	}
	if err := assetDirectory.RemoveDir(pendingName); err != nil {
		return ErrFilesystem
	}
	if err := assetDirectory.Sync(); err != nil {
		return ErrFilesystem
	}
	return nil
}

// Finalize commits the prepared desired set after the caller has verified the
// restarted runtime. It never rolls back or re-freezes after the database commit.
func (service *Service) Finalize(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	return service.FinalizeWhileLocked(ctx)
}

// FinalizeWhileLocked performs Finalize when the caller owns the process
// mutation coordinator.
func (service *Service) FinalizeWhileLocked(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	assetDirectory, err := service.files.OpenDir(service.assetDir)
	if err != nil {
		return fmt.Errorf("%w: open asset directory", ErrFilesystem)
	}
	defer assetDirectory.Close()

	stageDirectory, err := assetDirectory.OpenDir(pendingName)
	if errors.Is(err, os.ErrNotExist) {
		snapshot, snapshotErr := service.store.Snapshot(ctx)
		if snapshotErr != nil {
			return snapshotErr
		}
		if !snapshot.ConsolidationActive && equalStrings(snapshot.Desired, snapshot.Baseline) {
			return nil
		}
		return ErrPending
	}
	if err != nil {
		return ErrPending
	}
	defer stageDirectory.Close()
	stageInfo, err := stageDirectory.Stat()
	if err != nil || stageInfo == nil || !stageInfo.IsDir() || mode(stageInfo) != privateMode {
		return ErrManifest
	}
	stageUID, _, ownerErr := fileOwner(stageInfo)
	if ownerErr != nil || stageUID != os.Geteuid() {
		return ErrManifest
	}

	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return err
	}
	baselineCommitted := !snapshot.ConsolidationActive &&
		equalStrings(snapshot.Desired, snapshot.Baseline)
	manifestInfo, manifestStatErr := stageDirectory.Lstat(manifestName)
	if errors.Is(manifestStatErr, os.ErrNotExist) {
		if snapshot.ConsolidationActive || !equalStrings(snapshot.Desired, snapshot.Baseline) {
			return ErrManifest
		}
		if err := validatePartialStageWithoutManifest(stageDirectory); err != nil {
			return err
		}
		if err := stageDirectory.Close(); err != nil {
			if baselineCommitted {
				return &FinalizationCommittedError{Err: fmt.Errorf("%w: close pending directory", ErrFilesystem)}
			}
			return fmt.Errorf("%w: close pending directory", ErrFilesystem)
		}
		return service.cleanFinalizedStageDirectory(assetDirectory)
	}
	if manifestStatErr != nil || manifestInfo == nil {
		return ErrManifest
	}

	var manifest Manifest
	if snapshot.ConsolidationActive {
		manifest, err = service.readManifest(stageDirectory)
		if err == nil {
			err = service.validateStageContents(stageDirectory, manifest)
		}
	} else {
		manifest, err = service.readManifestMetadata(stageDirectory)
		if err == nil {
			err = service.validateFinalizedStageContents(stageDirectory, manifest)
		}
		if err == nil && (!equalStrings(snapshot.Desired, manifest.Domains) ||
			!equalStrings(snapshot.Baseline, manifest.Domains)) {
			return domainstore.ErrPreparedDomainsMismatch
		}
	}
	if err != nil {
		return err
	}

	if err := service.checkPairCandidates(ctx, assetDirectory, manifest); err != nil {
		return err
	}
	if snapshot.ConsolidationActive {
		if err := service.store.FinalizeConsolidation(ctx, manifest.Domains); err != nil {
			return fmt.Errorf("commit finalized domain baseline: %w", err)
		}
		baselineCommitted = true
	}
	if err := stageDirectory.Close(); err != nil {
		return fmt.Errorf("%w: close pending directory", ErrFilesystem)
	}
	if err := service.cleanFinalizedStageDirectory(assetDirectory); err != nil {
		if baselineCommitted {
			return &FinalizationCommittedError{Err: err}
		}
		return err
	}
	return nil
}

// FinalizationCommittedError indicates baseline commit succeeded but retryable
// private evidence cleanup did not; the live candidate must not be rolled back.
type FinalizationCommittedError struct {
	Err error
}

func (err *FinalizationCommittedError) Error() string {
	return fmt.Sprintf("geodata baseline committed; evidence cleanup failed: %v", err.Err)
}

func (err *FinalizationCommittedError) Unwrap() error {
	return err.Err
}

func normalizedDomains(input []string) ([]string, error) {
	onlyDomains, ips, err := domain.SplitResources(input)
	if err != nil {
		return nil, err
	}
	domains := append(onlyDomains, ips...)
	sort.Strings(domains)
	if len(domains) > maxDomains {
		return nil, ErrInvalidInput
	}
	for _, name := range domains {
		if name == emptyDomain {
			return nil, ErrReservedDomain
		}
	}
	return domains, nil
}

func domainInput(domains []string) string {
	if len(domains) == 0 {
		return "domain:" + emptyDomain + "\n"
	}
	var content strings.Builder
	for _, name := range domains {
		content.WriteString("domain:")
		content.WriteString(name)
		content.WriteByte('\n')
	}
	return content.String()
}

func (service *Service) createStage() (geodatainstall.Directory, geodatainstall.Directory, bool, error) {
	assetDirectory, err := service.files.OpenDir(service.assetDir)
	if err != nil {
		return nil, nil, false, fmt.Errorf("%w: open asset directory", ErrFilesystem)
	}
	if _, err := assetDirectory.Lstat(pendingName); err == nil {
		_ = assetDirectory.Close()
		return nil, nil, false, ErrPending
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = assetDirectory.Close()
		return nil, nil, false, ErrFilesystem
	}
	if err := assetDirectory.Mkdir(pendingName, privateMode); err != nil {
		_ = assetDirectory.Close()
		return nil, nil, false, fmt.Errorf("%w: create private pending area", ErrFilesystem)
	}
	if err := assetDirectory.Sync(); err != nil {
		return assetDirectory, nil, true, fmt.Errorf("%w: sync pending area creation", ErrFilesystem)
	}
	stage, err := assetDirectory.OpenDir(pendingName)
	if err != nil {
		return assetDirectory, nil, true, fmt.Errorf("%w: open pending area", ErrFilesystem)
	}
	if err := stage.Chmod(privateMode); err != nil {
		_ = stage.Close()
		return assetDirectory, nil, true, fmt.Errorf("%w: secure pending area", ErrFilesystem)
	}
	info, err := stage.Stat()
	if err != nil || info == nil || !info.IsDir() {
		_ = stage.Close()
		return assetDirectory, nil, true, ErrFilesystem
	}
	uid, _, err := fileOwner(info)
	if err != nil || uid != os.Geteuid() || mode(info) != privateMode {
		_ = stage.Close()
		return assetDirectory, nil, true, ErrFilesystem
	}
	return assetDirectory, stage, true, nil
}

func (service *Service) makeManifest(ctx context.Context, assetDirectory, stage geodatainstall.Directory, domains []string, candidateHash string) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		Version: manifestVersion, Domains: append([]string{}, domains...),
		GeositeName: service.candidateName, CandidateHash: candidateHash,
	}
	id, err := randomID()
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: create pending identifier", ErrFilesystem)
	}
	manifest.ID = id
	info, err := assetDirectory.Lstat(service.candidateName)
	if errors.Is(err, os.ErrNotExist) {
		return manifest, nil
	}
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return Manifest{}, ErrFilesystem
	}
	file, err := assetDirectory.OpenFile(service.candidateName, os.O_RDONLY, 0)
	if err != nil {
		return Manifest{}, ErrFilesystem
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		_ = file.Close()
		return Manifest{}, ErrFilesystem
	}
	uid, gid, err := fileOwner(opened)
	if err != nil {
		_ = file.Close()
		return Manifest{}, ErrFilesystem
	}
	backup, err := stage.OpenFile(service.backupName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		_ = file.Close()
		return Manifest{}, fmt.Errorf("%w: create exact previous-file backup", ErrFilesystem)
	}
	if err := backup.Chmod(privateFileMode); err != nil {
		_ = backup.Close()
		_ = file.Close()
		return Manifest{}, ErrFilesystem
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(backup, hash), io.LimitReader(contextReader{ctx: ctx, reader: file}, int64(service.maxAssetSize)+1))
	closeBackupErr := backup.Sync()
	closeBackupErr = errors.Join(closeBackupErr, backup.Close(), file.Close())
	if copyErr != nil || n > int64(service.maxAssetSize) || closeBackupErr != nil {
		return Manifest{}, errors.Join(ErrFilesystem, copyErr, closeBackupErr)
	}
	current, err := assetDirectory.Lstat(service.candidateName)
	if err != nil || !os.SameFile(info, current) {
		return Manifest{}, ErrFilesystem
	}
	manifest.PreviousExists = true
	manifest.PreviousHash = hex.EncodeToString(hash.Sum(nil))
	manifest.PreviousMode = uint32(opened.Mode().Perm() | opened.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
	manifest.PreviousUID = uid
	manifest.PreviousGID = gid
	if err := stage.Sync(); err != nil {
		return Manifest{}, fmt.Errorf("%w: sync previous-file backup", ErrFilesystem)
	}
	return manifest, nil
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

var errRenameAttempted = errors.New("live rename attempted")

func (service *Service) installCandidate(assetDirectory geodatainstall.Directory, content []byte, manifest Manifest) error {
	currentHash, exists, err := service.hashOptional(assetDirectory, service.candidateName)
	if err != nil {
		return err
	}
	if manifest.PreviousExists {
		if !exists || currentHash != manifest.PreviousHash {
			return ErrExternalDrift
		}
	} else if exists {
		return ErrExternalDrift
	}
	if _, err := assetDirectory.Lstat(service.liveCandidateName); err == nil {
		return ErrPending
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrFilesystem
	}
	candidate, err := assetDirectory.OpenFile(service.liveCandidateName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return fmt.Errorf("%w: create live candidate", ErrFilesystem)
	}
	_, err = io.Copy(candidate, bytes.NewReader(content))
	if err == nil {
		uid, gid := os.Geteuid(), os.Getegid()
		wantedMode := os.FileMode(installedMode)
		if manifest.PreviousExists {
			uid, gid = manifest.PreviousUID, manifest.PreviousGID
			wantedMode = os.FileMode(manifest.PreviousMode)
		}
		err = setOwnership(candidate, uid, gid)
		if err == nil {
			err = candidate.Chmod(wantedMode)
		}
		if err == nil {
			err = candidate.Sync()
		}
	}
	closeErr := candidate.Close()
	if err != nil || closeErr != nil {
		return errors.Join(ErrFilesystem, err, closeErr, service.removeFailedCandidate(assetDirectory))
	}
	if err := assetDirectory.Sync(); err != nil {
		return errors.Join(fmt.Errorf("%w: sync live candidate", ErrFilesystem), service.removeFailedCandidate(assetDirectory))
	}
	currentHash, exists, err = service.hashOptional(assetDirectory, service.candidateName)
	if err != nil {
		return err
	}
	if manifest.PreviousExists {
		if !exists || currentHash != manifest.PreviousHash {
			return errors.Join(ErrExternalDrift, service.removeFailedCandidate(assetDirectory))
		}
	} else if exists {
		return errors.Join(ErrExternalDrift, service.removeFailedCandidate(assetDirectory))
	}
	if err := assetDirectory.Rename(service.liveCandidateName, service.candidateName); err != nil {
		return errors.Join(errRenameAttempted, ErrFilesystem)
	}
	if err := assetDirectory.Sync(); err != nil {
		return errors.Join(errRenameAttempted, fmt.Errorf("%w: sync installed geosite", ErrFilesystem))
	}
	return nil
}

func (service *Service) cleanStage() error {
	assetDirectory, err := service.files.OpenDir(service.assetDir)
	if err != nil {
		return ErrFilesystem
	}
	defer assetDirectory.Close()
	return service.cleanStageDirectory(assetDirectory)
}

func (service *Service) cleanStageDirectory(assetDirectory geodatainstall.Directory) error {
	return service.cleanStageDirectoryWithManifestLast(assetDirectory, false)
}

func (service *Service) cleanFinalizedStageDirectory(assetDirectory geodatainstall.Directory) error {
	return service.cleanStageDirectoryWithManifestLast(assetDirectory, true)
}

func (service *Service) cleanStageDirectoryWithManifestLast(assetDirectory geodatainstall.Directory, manifestLast bool) error {
	stage, err := assetDirectory.OpenDir(pendingName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrFilesystem
	}
	entries, err := stage.ReadDir()
	if err != nil {
		_ = stage.Close()
		return ErrFilesystem
	}
	manifestPresent := false
	for _, name := range entries {
		if !service.allowedStageEntry(name) {
			_ = stage.Close()
			return ErrPending
		}
		if manifestLast && name == manifestName {
			manifestPresent = true
			continue
		}
		if err := stage.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = stage.Close()
			return fmt.Errorf("%w: remove staged file", ErrFilesystem)
		}
	}
	if err := stage.Sync(); err != nil {
		_ = stage.Close()
		return fmt.Errorf("%w: sync pending cleanup", ErrFilesystem)
	}
	if manifestLast && manifestPresent {
		if err := stage.Remove(manifestName); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = stage.Close()
			return fmt.Errorf("%w: remove staged manifest", ErrFilesystem)
		}
		if err := stage.Sync(); err != nil {
			_ = stage.Close()
			return fmt.Errorf("%w: sync pending manifest cleanup", ErrFilesystem)
		}
	}
	if err := stage.Close(); err != nil {
		return ErrFilesystem
	}
	if err := assetDirectory.RemoveDir(pendingName); err != nil {
		return fmt.Errorf("%w: remove pending area", ErrFilesystem)
	}
	if err := assetDirectory.Sync(); err != nil {
		return fmt.Errorf("%w: sync pending-area removal", ErrFilesystem)
	}
	return nil
}

func (service *Service) allowedStageEntry(name string) bool {
	switch name {
	case inputName, service.candidateName, service.backupName, service.geoipName, service.geoipName + ".previous", manifestName, manifestCandidate:
		return true
	default:
		return false
	}
}

func (service *Service) readBoundedRegular(directory geodatainstall.Directory, name string) ([]byte, string, error) {
	return service.readBoundedRegularContext(context.Background(), directory, name)
}

func (service *Service) readBoundedRegularContext(ctx context.Context, directory geodatainstall.Directory, name string) ([]byte, string, error) {
	info, err := directory.Lstat(name)
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return nil, "", ErrBuilderOutput
	}
	if info.Size() > int64(service.maxAssetSize) {
		return nil, "", &geodatalimits.Exceeded{LimitBytes: service.maxAssetSize}
	}
	file, err := directory.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, "", ErrBuilderOutput
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, "", ErrBuilderOutput
	}
	hash := sha256.New()
	content, err := io.ReadAll(io.LimitReader(io.TeeReader(
		contextReader{ctx: ctx, reader: file}, hash), int64(service.maxAssetSize)+1))
	closeErr := file.Close()
	if err != nil || closeErr != nil || len(content) == 0 ||
		len(content) > service.maxAssetSize || int64(len(content)) != opened.Size() {
		return nil, "", ErrBuilderOutput
	}
	return content, hex.EncodeToString(hash.Sum(nil)), nil
}

func writeFile(directory geodatainstall.Directory, name string, content []byte, mode os.FileMode) error {
	file, err := directory.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	err = file.Chmod(mode)
	if err == nil {
		_, err = io.Copy(file, bytes.NewReader(content))
	}
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

func writeManifest(directory geodatainstall.Directory, manifest Manifest) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err != nil || len(data) > maxManifestSize {
		return ErrManifest
	}
	if err := writeFile(directory, manifestCandidate, data, privateFileMode); err != nil {
		return err
	}
	return directory.Rename(manifestCandidate, manifestName)
}

func (service *Service) readManifest(directory geodatainstall.Directory) (Manifest, error) {
	manifest, err := service.readManifestMetadata(directory)
	if err != nil {
		return Manifest{}, err
	}
	if err := service.validateStageArtifacts(directory, manifest, false); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (service *Service) readManifestMetadata(directory geodatainstall.Directory) (Manifest, error) {
	info, err := directory.Lstat(manifestName)
	if err != nil || info == nil || !info.Mode().IsRegular() || mode(info) != privateFileMode {
		return Manifest{}, ErrManifest
	}
	file, err := directory.OpenFile(manifestName, os.O_RDONLY, 0)
	if err != nil {
		return Manifest{}, ErrManifest
	}
	defer file.Close()
	opened, err := file.Stat()
	uid, _, ownerErr := fileOwner(info)
	if err != nil || !os.SameFile(info, opened) || ownerErr != nil || uid != os.Geteuid() {
		return Manifest{}, ErrManifest
	}
	content, err := io.ReadAll(io.LimitReader(file, maxManifestSize+1))
	if err != nil || len(content) > maxManifestSize || !utf8.Valid(content) {
		return Manifest{}, ErrManifest
	}
	if rejectDuplicateKeys(content) != nil {
		return Manifest{}, ErrManifest
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(content, &shape); err != nil || !exactManifestKeys(shape) {
		return Manifest{}, ErrManifest
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil || validateManifest(manifest) != nil {
		return Manifest{}, ErrManifest
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Manifest{}, ErrManifest
	}
	if manifest.GeositeName != service.candidateName || manifest.Geoip.Name != service.geoipName {
		return Manifest{}, ErrManifest
	}
	return manifest, nil
}

func (service *Service) validateStageArtifacts(directory geodatainstall.Directory, manifest Manifest, allowMissing bool) error {
	names := []string{service.candidateName, inputName, service.geoipName}
	if manifest.PreviousExists {
		names = append(names, service.backupName)
	}
	if manifest.Geoip.PreviousExists {
		names = append(names, manifest.Geoip.BackupName)
	}
	for _, name := range names {
		info, err := directory.Lstat(name)
		if errors.Is(err, os.ErrNotExist) && allowMissing {
			continue
		}
		if err != nil || info == nil || !info.Mode().IsRegular() {
			return ErrManifest
		}
		uid, _, err := fileOwner(info)
		if err != nil || uid != os.Geteuid() {
			return ErrManifest
		}
		if (name == service.backupName || name == service.geoipName+".previous") && mode(info) != privateFileMode {
			return ErrManifest
		}
		content, hash, err := service.readBoundedRegular(directory, name)
		if err != nil || len(content) == 0 {
			return ErrManifest
		}
		switch name {
		case service.candidateName:
			if hash != manifest.CandidateHash {
				return ErrManifest
			}
		case inputName:
			onlyDomains, _, err := domain.SplitResources(manifest.Domains)
			if err != nil || string(content) != domainInput(onlyDomains) {
				return ErrManifest
			}
		case service.geoipName:
			_, ips, err := domain.SplitResources(manifest.Domains)
			expected, encodeErr := usergeoip.Encode(ips)
			if err != nil || encodeErr != nil || hash != manifest.Geoip.InstalledHash || !bytes.Equal(content, expected) {
				return ErrManifest
			}
		case manifest.Geoip.BackupName:
			if hash != manifest.Geoip.PreviousHash {
				return ErrManifest
			}
		case service.backupName:
			if hash != manifest.PreviousHash {
				return ErrManifest
			}
		}
	}
	if !manifest.PreviousExists {
		if _, err := directory.Lstat(service.backupName); !errors.Is(err, os.ErrNotExist) {
			return ErrManifest
		}
	}
	if !manifest.Geoip.PreviousExists {
		if _, err := directory.Lstat(manifest.Geoip.BackupName); !errors.Is(err, os.ErrNotExist) {
			return ErrManifest
		}
	}
	return nil
}

func (service *Service) validateStageContents(directory geodatainstall.Directory, manifest Manifest) error {
	entries, err := directory.ReadDir()
	if err != nil {
		return ErrManifest
	}
	expected := map[string]struct{}{
		manifestName: {}, service.candidateName: {}, inputName: {}, service.geoipName: {},
	}
	if manifest.PreviousExists {
		expected[service.backupName] = struct{}{}
	}
	if manifest.Geoip.PreviousExists {
		expected[manifest.Geoip.BackupName] = struct{}{}
	}
	if len(entries) != len(expected) {
		return ErrManifest
	}
	for _, entry := range entries {
		if _, ok := expected[entry]; !ok {
			return ErrManifest
		}
		delete(expected, entry)
	}
	if len(expected) != 0 {
		return ErrManifest
	}
	return nil
}

func (service *Service) validateFinalizedStageContents(directory geodatainstall.Directory, manifest Manifest) error {
	entries, err := directory.ReadDir()
	if err != nil {
		return ErrManifest
	}
	expected := map[string]struct{}{
		manifestName: {}, service.candidateName: {}, inputName: {}, service.geoipName: {},
	}
	if manifest.PreviousExists {
		expected[service.backupName] = struct{}{}
	}
	if manifest.Geoip.PreviousExists {
		expected[manifest.Geoip.BackupName] = struct{}{}
	}
	for _, entry := range entries {
		if _, ok := expected[entry]; !ok {
			return ErrManifest
		}
		delete(expected, entry)
	}
	if _, exists := expected[manifestName]; exists {
		return ErrManifest
	}
	return service.validateStageArtifacts(directory, manifest, true)
}

func validatePartialStageWithoutManifest(directory geodatainstall.Directory) error {
	entries, err := directory.ReadDir()
	if err != nil {
		return ErrManifest
	}
	if len(entries) != 0 {
		return ErrManifest
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	if manifest.Version != manifestVersion || !validID(manifest.ID) || !validHash(manifest.CandidateHash) ||
		!bootstrap.ValidUserGeositeName(manifest.GeositeName) || manifest.Domains == nil || validateIPRecord(manifest.Geoip) != nil {
		return ErrManifest
	}
	domains, err := normalizedDomains(manifest.Domains)
	if err != nil || !equalStrings(domains, manifest.Domains) {
		return ErrManifest
	}
	if manifest.PreviousExists {
		if !validHash(manifest.PreviousHash) || manifest.PreviousUID < 0 || manifest.PreviousGID < 0 ||
			uint64(manifest.PreviousUID) > uint64(^uint32(0)) ||
			uint64(manifest.PreviousGID) > uint64(^uint32(0)) ||
			manifest.PreviousMode&^uint32(07777) != 0 {
			return ErrManifest
		}
	} else if manifest.PreviousHash != "" || manifest.PreviousMode != 0 ||
		manifest.PreviousUID != 0 || manifest.PreviousGID != 0 {
		return ErrManifest
	}
	return nil
}

func exactManifestKeys(object map[string]json.RawMessage) bool {
	keys := []string{
		"version", "id", "domains", "geositeName", "candidateHash", "previousExists",
		"previousHash", "previousMode", "previousUID", "previousGID", "geoip",
	}
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	var ip map[string]json.RawMessage
	if json.Unmarshal(object["geoip"], &ip) != nil || len(ip) != 8 {
		return false
	}
	for _, key := range []string{"name", "installedHash", "previousExists", "previousHash", "backupName", "previousMode", "previousUID", "previousGID"} {
		if _, ok := ip[key]; !ok {
			return false
		}
	}
	return true
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSON(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrManifest
	}
	return nil
}

func scanJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
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
				return ErrManifest
			}
			if _, exists := seen[key]; exists {
				return ErrManifest
			}
			seen[key] = struct{}{}
			if err := scanJSON(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSON(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return ErrManifest
	}
}

func (service *Service) hashOptional(directory geodatainstall.Directory, name string) (string, bool, error) {
	return service.hashOptionalContext(context.Background(), directory, name)
}

func (service *Service) hashOptionalContext(ctx context.Context, directory geodatainstall.Directory, name string) (string, bool, error) {
	info, err := directory.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return "", false, ErrExternalDrift
	}
	content, hash, err := service.readBoundedRegularContext(ctx, directory, name)
	if err != nil || len(content) == 0 {
		return "", false, ErrExternalDrift
	}
	return hash, true, nil
}

func (service *Service) atomicRestore(directory geodatainstall.Directory, content []byte, manifest Manifest) error {
	file, err := directory.OpenFile(service.liveCandidateName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return err
	}
	_, writeErr := io.Copy(file, bytes.NewReader(content))
	if writeErr == nil {
		writeErr = setOwnership(file, manifest.PreviousUID, manifest.PreviousGID)
	}
	if writeErr == nil {
		writeErr = file.Chmod(os.FileMode(manifest.PreviousMode))
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = directory.Remove(service.liveCandidateName)
		return errors.Join(writeErr, closeErr)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Remove(service.liveCandidateName)
		return err
	}
	if err := directory.Rename(service.liveCandidateName, service.candidateName); err != nil {
		_ = directory.Remove(service.liveCandidateName)
		return err
	}
	return directory.Sync()
}

func setOwnership(file geodatainstall.File, uid, gid int) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	currentUID, currentGID, err := fileOwner(info)
	if err != nil {
		return err
	}
	if uid == currentUID && gid == currentGID {
		return nil
	}
	return file.Chown(uid, gid)
}

func fileOwner(info os.FileInfo) (int, int, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("file ownership is unavailable")
	}
	return int(stat.Uid), int(stat.Gid), nil
}

func mode(info os.FileInfo) os.FileMode {
	return info.Mode().Perm() | info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
}

func hashBytes(content []byte) string {
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:])
}

func validHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func validID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && strings.ToLower(value) == value
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func equalStrings(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

type consolidationOSFileSystem struct{}

func (consolidationOSFileSystem) OpenDir(path string) (geodatainstall.Directory, error) {
	return geodatainstall.OpenDirectory(path)
}

func (consolidationOSFileSystem) OpenSource(path string) (geodatainstall.File, error) {
	return geodatainstall.OpenSourceFile(path)
}

func firstLimitMB(values []int) int {
	if len(values) > 0 {
		return values[0]
	}
	return 0
}
