// Package geodataupdate coordinates one serialized geodata replacement and
// runtime activation transaction.
package geodataupdate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/consolidation"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/geodatadownload"
	"home-router/xray-core-helper/internal/geodatainstall"
	"home-router/xray-core-helper/internal/servicecontrol"
)

type Downloader interface {
	Download(context.Context, bootstrap.GeodataConfig) (*geodatadownload.Result, error)
}

type Validator interface {
	Validate(context.Context, *geodatadownload.Result) error
}

type Installer interface {
	InstallOwnedWhileLocked(context.Context, geodatainstall.Sources) (geodatainstall.Manifest, geodatainstall.InstallRecovery, error)
	VerifyWhileLocked(context.Context) error
	RollbackWhileLocked(context.Context) error
	CleanupOrphansWhileLocked(context.Context) error
	CommitWhileLocked(context.Context) error
}

type Consolidation interface {
	PrepareWhileLocked(context.Context) error
	RollbackWhileLocked(context.Context) error
	VerifyWhileLocked(context.Context) error
	FinalizeWhileLocked(context.Context) error
}

type Restarter interface {
	RestartWhileLocked(context.Context) error
}

type HealthVerifier interface {
	VerifyActive(context.Context) error
}

type RuntimeOverlay interface {
	ApplyWhileLocked(context.Context) error
}

type ServiceController interface {
	Execute(context.Context, servicecontrol.Unit, servicecontrol.Action) (servicecontrol.Result, error)
}

type RoutingHealth interface {
	ListRules(context.Context) ([]byte, error)
}

var (
	ErrDownloadFailed   = errors.New("geodata download phase failed")
	ErrValidationFailed = errors.New("geodata validation phase failed")
	ErrInstallFailed    = errors.New("geodata installation phase failed")
	ErrRestartFailed    = errors.New("geodata restart or activation phase failed")
	ErrRollbackFailed   = errors.New("geodata rollback phase failed")
)

type Options struct {
	Downloader     Downloader
	Validator      Validator
	Installer      Installer
	Consolidation  Consolidation
	Restarter      Restarter
	HealthVerifier HealthVerifier
	RuntimeOverlay RuntimeOverlay
}

type Updater struct {
	downloader Downloader
	validator  Validator
	installer  Installer
	consol     Consolidation
	restarter  Restarter
	health     HealthVerifier
	overlay    RuntimeOverlay
}

func New(options Options) (*Updater, error) {
	if options.Downloader == nil || options.Validator == nil || options.Installer == nil ||
		options.Consolidation == nil || options.Restarter == nil ||
		options.HealthVerifier == nil || options.RuntimeOverlay == nil {
		return nil, errors.New("geodata update dependencies are required")
	}
	return &Updater{
		downloader: options.Downloader,
		validator:  options.Validator,
		installer:  options.Installer,
		consol:     options.Consolidation,
		restarter:  options.Restarter,
		health:     options.HealthVerifier,
		overlay:    options.RuntimeOverlay,
	}, nil
}

// Update performs one planned restart for the complete candidate set. Child
// operations use their explicitly locked boundaries so the shared coordinator
// remains held across prepare, download, install, restart, overlay and finalize.
func (updater *Updater) Update(ctx context.Context, config bootstrap.GeodataConfig) (resultErr error) {
	if ctx == nil {
		return errors.New("geodata update context is required")
	}
	lease, err := coordinator.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()

	var downloaded *geodatadownload.Result
	defer func() {
		if downloaded == nil {
			return
		}
		if cleanupErr := downloaded.Cleanup(); cleanupErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("clean downloaded geodata: %w", cleanupErr))
		}
	}()

	if err := updater.consol.PrepareWhileLocked(ctx); err != nil {
		return fmt.Errorf("prepare consolidation: %w", err)
	}
	consolidationOwned := true
	downloaded, err = updater.downloader.Download(ctx, config)
	if err != nil {
		return updater.rollback(ctx, consolidationOwned, false, false, false,
			errors.Join(ErrDownloadFailed, fmt.Errorf("download geodata: %w", err)))
	}
	if downloaded == nil {
		return updater.rollback(ctx, consolidationOwned, false, false, false,
			errors.Join(ErrDownloadFailed, errors.New("download returned no geodata result")))
	}
	if err := updater.validator.Validate(ctx, downloaded); err != nil {
		return updater.rollback(ctx, consolidationOwned, false, false, false,
			errors.Join(ErrValidationFailed, fmt.Errorf("validate geodata candidates: %w", err)))
	}

	sources := geodatainstall.Sources{
		GeositePath: downloaded.GeositePath,
		GeoipPath:   downloaded.GeoipPath,
	}
	_, standardRecovery, err := updater.installer.InstallOwnedWhileLocked(ctx, sources)
	if err != nil {
		cause := errors.Join(ErrInstallFailed, fmt.Errorf("install geodata candidates: %w", err))
		switch standardRecovery {
		case geodatainstall.ManifestRecovery:
			return updater.rollback(ctx, consolidationOwned, true, false, false, cause)
		case geodatainstall.CleanupOnlyRecovery:
			return updater.rollback(ctx, consolidationOwned, false, true, false, cause)
		default:
			return updater.rollback(ctx, consolidationOwned, false, false, false, cause)
		}
	}
	if standardRecovery != geodatainstall.ManifestRecovery {
		return updater.rollback(ctx, consolidationOwned, false, false, false,
			errors.New("installer succeeded without establishing transaction ownership"))
	}
	standardOwned := true

	restartMayHaveRun := true
	if err := updater.restarter.RestartWhileLocked(ctx); err != nil {
		return updater.rollback(ctx, consolidationOwned, standardOwned, false, restartMayHaveRun,
			errors.Join(ErrRestartFailed, fmt.Errorf("restart Xray for candidate geodata: %w", err)))
	}
	if err := updater.health.VerifyActive(ctx); err != nil {
		return updater.rollback(ctx, consolidationOwned, standardOwned, false, restartMayHaveRun,
			errors.Join(ErrRestartFailed, fmt.Errorf("verify Xray after candidate restart: %w", err)))
	}
	if err := updater.overlay.ApplyWhileLocked(ctx); err != nil {
		return updater.rollback(ctx, consolidationOwned, standardOwned, false, restartMayHaveRun,
			fmt.Errorf("apply runtime domain overlay: %w", err))
	}
	if err := updater.installer.VerifyWhileLocked(ctx); err != nil {
		return updater.rollback(ctx, consolidationOwned, standardOwned, false, restartMayHaveRun,
			fmt.Errorf("verify installed standard geodata: %w", err))
	}
	if err := updater.consol.VerifyWhileLocked(ctx); err != nil {
		return updater.rollback(ctx, consolidationOwned, standardOwned, false, restartMayHaveRun,
			fmt.Errorf("verify installed custom geodata: %w", err))
	}
	if err := updater.consol.FinalizeWhileLocked(ctx); err != nil {
		var committed *consolidation.FinalizationCommittedError
		if errors.As(err, &committed) {
			cleanupContext, cancel := recoveryContext(ctx)
			defer cancel()
			cleanupErr := updater.installer.CommitWhileLocked(cleanupContext)
			if cleanupErr != nil {
				return errors.Join(
					fmt.Errorf("finalize geodata baseline: %w", err),
					fmt.Errorf("commit installed geodata evidence: %w", cleanupErr),
				)
			}
			return fmt.Errorf("finalize geodata baseline: %w", err)
		}
		return updater.rollback(ctx, consolidationOwned, standardOwned, false, restartMayHaveRun,
			fmt.Errorf("finalize geodata baseline: %w", err))
	}
	cleanupContext, cancel := recoveryContext(ctx)
	defer cancel()
	if err := updater.installer.CommitWhileLocked(cleanupContext); err != nil {
		return fmt.Errorf("commit installed geodata evidence: %w", err)
	}
	return nil
}

func (updater *Updater) rollback(
	ctx context.Context,
	consolidationOwned, standardOwned, standardCleanupOnly bool,
	restartMayHaveRun bool,
	cause error,
) error {
	recoveryContext, cancel := recoveryContext(ctx)
	defer cancel()
	var failures []error
	failures = append(failures, cause)
	if standardOwned {
		if err := updater.installer.RollbackWhileLocked(recoveryContext); err != nil {
			failures = append(failures, fmt.Errorf("rollback standard geodata: %w", err))
		}
	}
	if standardCleanupOnly {
		if err := updater.installer.CleanupOrphansWhileLocked(recoveryContext); err != nil {
			failures = append(failures, fmt.Errorf("clean owned standard geodata evidence: %w", err))
		}
	}
	if consolidationOwned {
		if err := updater.consol.RollbackWhileLocked(recoveryContext); err != nil {
			failures = append(failures, fmt.Errorf("rollback custom geodata and domain state: %w", err))
		}
	}
	if restartMayHaveRun {
		if err := updater.restarter.RestartWhileLocked(recoveryContext); err != nil {
			failures = append(failures, fmt.Errorf("restart Xray after geodata rollback: %w", err))
		}
	}
	if len(failures) > 1 {
		return errors.Join(append([]error{ErrRollbackFailed}, failures...)...)
	}
	return errors.Join(failures...)
}

func recoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
}
