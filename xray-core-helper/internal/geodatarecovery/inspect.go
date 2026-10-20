// Package geodatarecovery classifies persisted geodata transaction evidence.
package geodatarecovery

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"home-router/xray-core-helper/internal/consolidation"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/geodatainstall"
)

type Phase uint8

const (
	PhaseClean Phase = iota
	PhaseInspectionFailed
	PhaseInconsistent
	PhaseStandardPartial
	PhaseStandardInstalled
	PhaseStandardCleanup
	PhaseCustomFreezePartial
	PhaseCustomPreparedPrevious
	PhaseCustomPreparedCandidate
	PhaseCustomFinalizedCleanup
	PhaseCustomPartialCleanup
	PhaseCombinedRollback
	PhaseCombinedInstalled
	PhaseCombinedCleanup
	PhaseCustomPreparedMixed
)

func (phase Phase) String() string {
	names := [...]string{
		"clean", "inspection_failed", "inconsistent", "standard_partial_install",
		"standard_installed", "standard_cleanup_only", "custom_freeze_partial",
		"custom_prepared_previous_live", "custom_prepared_candidate_live",
		"custom_finalized_cleanup", "custom_partial_cleanup", "combined_rollback",
		"combined_installed", "combined_cleanup", "custom_prepared_mixed_live",
	}
	if int(phase) >= len(names) {
		return "unknown"
	}
	return names[phase]
}

func (phase Phase) MarshalJSON() ([]byte, error) {
	return json.Marshal(phase.String())
}

type Recovery uint8

const (
	RecoveryNone Recovery = iota
	RecoveryClearCustomFreeze
	RecoveryRollbackStandard
	RecoveryCleanupStandard
	RecoveryRollbackCustom
	RecoveryCleanupCustom
	RecoveryRollbackBoth
	RecoveryCleanupBoth
)

func (recovery Recovery) String() string {
	names := [...]string{
		"none", "clear_custom_freeze", "rollback_standard_geodata", "cleanup_standard_evidence",
		"rollback_custom_geodata", "cleanup_custom_evidence",
		"rollback_both_geodata_transactions", "cleanup_both_geodata_transactions",
	}
	if int(recovery) >= len(names) {
		return "unknown"
	}
	return names[recovery]
}

func (recovery Recovery) MarshalJSON() ([]byte, error) {
	return json.Marshal(recovery.String())
}

const (
	ActionReviewEvidence    = "review_geodata_filesystem_evidence"
	ActionReviewDrift       = "review_external_geodata_drift"
	ActionChooseDirection   = "decide_geodata_recovery_direction"
	ActionReviewDomainState = "review_domain_consolidation_state"
	ActionRetryInspection   = "retry_geodata_inspection"
	ActionRetryRecovery     = "retry_geodata_recovery"
)

type State struct {
	Phase Phase `json:"phase"`
}

type Plan struct {
	Phase             Phase    `json:"phase"`
	WritesBlocked     bool     `json:"writesBlocked"`
	AutomaticRecovery bool     `json:"automaticRecovery"`
	Recovery          Recovery `json:"recovery"`
	OperatorAction    string   `json:"operatorAction,omitempty"`
}

type Result struct {
	State State `json:"state"`
	Plan  Plan  `json:"plan"`
}

type StandardInspector interface {
	Inspect(context.Context) (geodatainstall.Inspection, error)
}

type CustomInspector interface {
	Inspect(context.Context) (consolidation.Inspection, error)
}

type DomainSnapshotter interface {
	Snapshot(context.Context) (domainstore.Snapshot, error)
}

type Options struct {
	Standard StandardInspector
	Custom   CustomInspector
	Domains  DomainSnapshotter
}

type Inspector struct {
	standard StandardInspector
	custom   CustomInspector
	domains  DomainSnapshotter
}

func New(options Options) (*Inspector, error) {
	if options.Standard == nil || options.Custom == nil || options.Domains == nil {
		return nil, errors.New("geodata recovery inspectors are required")
	}
	return &Inspector{standard: options.Standard, custom: options.Custom, domains: options.Domains}, nil
}

// Inspect returns a redacted, deterministic projection. Source errors are
// deliberately reduced to a stable operator action and never enter the result.
func (inspector *Inspector) Inspect(ctx context.Context) Result {
	result, _, _, _, _ := inspector.inspect(ctx)
	return result
}

func (inspector *Inspector) inspect(ctx context.Context) (
	Result, geodatainstall.Inspection, consolidation.Inspection, domainstore.Snapshot, error,
) {
	if ctx == nil || ctx.Err() != nil {
		return result(PhaseInspectionFailed, RecoveryNone, ActionRetryInspection),
			geodatainstall.Inspection{}, consolidation.Inspection{}, domainstore.Snapshot{}, errors.New("inspection unavailable")
	}
	standard, err := inspector.standard.Inspect(ctx)
	if err != nil {
		return result(PhaseInspectionFailed, RecoveryNone, ActionRetryInspection),
			geodatainstall.Inspection{}, consolidation.Inspection{}, domainstore.Snapshot{}, err
	}
	custom, err := inspector.custom.Inspect(ctx)
	if err != nil {
		return result(PhaseInspectionFailed, RecoveryNone, ActionRetryInspection),
			standard, consolidation.Inspection{}, domainstore.Snapshot{}, err
	}
	domains, err := inspector.domains.Snapshot(ctx)
	if err != nil {
		return result(PhaseInspectionFailed, RecoveryNone, ActionRetryInspection),
			standard, custom, domainstore.Snapshot{}, err
	}
	return classify(standard, custom, domains), standard, custom, domains, nil
}

func classify(
	standard geodatainstall.Inspection,
	custom consolidation.Inspection,
	domains domainstore.Snapshot,
) Result {
	if standard.Phase == geodatainstall.InspectionInvalid ||
		custom.Phase == consolidation.InspectionInvalid {
		return result(PhaseInconsistent, RecoveryNone, ActionReviewEvidence)
	}
	if custom.TransientPresent {
		return result(PhaseInconsistent, RecoveryNone, ActionReviewEvidence)
	}

	standardRecovery, standardPhase, standardAmbiguous := classifyStandard(standard)
	customRecovery, customPhase, customAmbiguous := classifyCustom(custom, domains)
	if domains.ConsolidationActive &&
		standard.Phase == geodatainstall.InspectionCleanupOnly {
		return result(PhaseInconsistent, RecoveryNone, ActionReviewEvidence)
	}
	if standard.Phase == geodatainstall.InspectionManifest &&
		(custom.Phase == consolidation.InspectionPartial ||
			(domains.ConsolidationActive && custom.Phase == consolidation.InspectionClean)) {
		return result(PhaseInconsistent, RecoveryNone, ActionReviewEvidence)
	}
	if standard.Phase == geodatainstall.InspectionManifest &&
		standardRecovery == RecoveryRollbackStandard &&
		custom.Phase == consolidation.InspectionManifest && !domains.ConsolidationActive {
		return result(PhaseInconsistent, RecoveryNone, ActionReviewEvidence)
	}
	if customAmbiguous == "" &&
		standard.Phase == geodatainstall.InspectionManifest &&
		custom.Phase == consolidation.InspectionManifest &&
		domains.ConsolidationActive && custom.Live != consolidation.LiveGeositeCandidate &&
		!(custom.Live == consolidation.LiveAssetsMixed && standardRecovery == RecoveryRollbackStandard) {
		return result(PhaseInconsistent, RecoveryNone, ActionReviewEvidence)
	}
	if customAmbiguous != "" {
		return result(customPhase, RecoveryNone, customAmbiguous)
	}
	if customRecovery == RecoveryClearCustomFreeze && standardRecovery != RecoveryNone {
		return result(PhaseInconsistent, RecoveryNone, ActionReviewEvidence)
	}

	if standardAmbiguous != "" {
		if standardPhase == PhaseStandardInstalled &&
			custom.Phase == consolidation.InspectionManifest &&
			!domains.ConsolidationActive && customRecovery == RecoveryCleanupCustom {
			return result(PhaseCombinedCleanup, RecoveryCleanupBoth, "")
		}
		if standardPhase == PhaseStandardInstalled &&
			custom.Phase == consolidation.InspectionManifest &&
			domains.ConsolidationActive &&
			custom.Live == consolidation.LiveGeositeCandidate {
			return result(PhaseCombinedInstalled, RecoveryNone, ActionChooseDirection)
		}
		return result(standardPhase, RecoveryNone, standardAmbiguous)
	}

	if standard.Phase == geodatainstall.InspectionManifest &&
		custom.Phase == consolidation.InspectionManifest &&
		domains.ConsolidationActive {
		if standardRecovery == RecoveryRollbackStandard &&
			customRecovery == RecoveryRollbackCustom {
			return result(PhaseCombinedRollback, RecoveryRollbackBoth, "")
		}
		if standardRecovery == RecoveryNone {
			return result(PhaseCombinedInstalled, RecoveryNone, ActionChooseDirection)
		}
		return result(PhaseInconsistent, RecoveryNone, ActionReviewEvidence)
	}

	switch {
	case standardRecovery == RecoveryCleanupStandard &&
		customRecovery == RecoveryCleanupCustom:
		return result(PhaseCombinedCleanup, RecoveryCleanupBoth, "")
	case standardRecovery == RecoveryCleanupStandard:
		return result(standardPhase, standardRecovery, "")
	case customRecovery == RecoveryCleanupCustom:
		return result(customPhase, customRecovery, "")
	case standardRecovery == RecoveryRollbackStandard:
		return result(standardPhase, standardRecovery, "")
	case customRecovery == RecoveryClearCustomFreeze:
		return result(customPhase, customRecovery, "")
	case customRecovery == RecoveryRollbackCustom:
		if standardRecovery == RecoveryRollbackStandard {
			return result(PhaseCombinedRollback, RecoveryRollbackBoth, "")
		}
		if standardRecovery != RecoveryNone {
			return result(PhaseInconsistent, RecoveryNone, ActionReviewEvidence)
		}
		return result(customPhase, customRecovery, "")
	default:
		return result(PhaseClean, RecoveryNone, "")
	}
}

func classifyStandard(inspection geodatainstall.Inspection) (Recovery, Phase, string) {
	switch inspection.Phase {
	case geodatainstall.InspectionClean:
		return RecoveryNone, PhaseClean, ""
	case geodatainstall.InspectionCleanupOnly:
		return RecoveryCleanupStandard, PhaseStandardCleanup, ""
	case geodatainstall.InspectionInvalid:
		return RecoveryNone, PhaseInconsistent, ActionReviewEvidence
	case geodatainstall.InspectionManifest:
		if inspection.Manifest == nil {
			return RecoveryNone, PhaseInconsistent, ActionReviewEvidence
		}
		previous := false
		installed := true
		for index, record := range inspection.Manifest.Assets {
			switch inspection.Live[index] {
			case geodatainstall.LiveAssetInstalled:
			case geodatainstall.LiveAssetPrevious:
				if !record.PreviousExists {
					return RecoveryNone, PhaseInconsistent, ActionReviewEvidence
				}
				previous = true
				installed = false
			case geodatainstall.LiveAssetMissing:
				if record.PreviousExists {
					return RecoveryNone, PhaseInconsistent, ActionReviewEvidence
				}
				previous = true
				installed = false
			case geodatainstall.LiveAssetDrift:
				return RecoveryNone, PhaseInconsistent, ActionReviewDrift
			default:
				return RecoveryNone, PhaseInconsistent, ActionReviewEvidence
			}
		}
		if installed {
			return RecoveryNone, PhaseStandardInstalled, ActionChooseDirection
		}
		if previous {
			return RecoveryRollbackStandard, PhaseStandardPartial, ""
		}
	}
	return RecoveryNone, PhaseInconsistent, ActionReviewEvidence
}

func classifyCustom(
	inspection consolidation.Inspection,
	domains domainstore.Snapshot,
) (Recovery, Phase, string) {
	switch inspection.Phase {
	case consolidation.InspectionInvalid:
		return RecoveryNone, PhaseInconsistent, ActionReviewEvidence
	case consolidation.InspectionClean:
		if domains.ConsolidationActive {
			return RecoveryClearCustomFreeze, PhaseCustomFreezePartial, ""
		}
		return RecoveryNone, PhaseClean, ""
	case consolidation.InspectionPartial:
		if domains.ConsolidationActive {
			return RecoveryClearCustomFreeze, PhaseCustomFreezePartial, ""
		}
		return RecoveryCleanupCustom, PhaseCustomPartialCleanup, ""
	case consolidation.InspectionManifest:
		if inspection.Manifest == nil {
			return RecoveryNone, PhaseInconsistent, ActionReviewEvidence
		}
		if domains.ConsolidationActive {
			if !reflect.DeepEqual(domains.Desired, inspection.Manifest.Domains) {
				return RecoveryNone, PhaseInconsistent, ActionReviewDomainState
			}
			if !inspection.PreparedEvidence {
				return RecoveryNone, PhaseInconsistent, ActionReviewEvidence
			}
			switch inspection.Live {
			case consolidation.LiveGeositePrevious, consolidation.LiveGeositeAbsent:
				return RecoveryRollbackCustom, PhaseCustomPreparedPrevious, ""
			case consolidation.LiveGeositeCandidate:
				return RecoveryRollbackCustom, PhaseCustomPreparedCandidate, ""
			case consolidation.LiveAssetsMixed:
				return RecoveryRollbackCustom, PhaseCustomPreparedMixed, ""
			case consolidation.LiveGeositeDrift:
				return RecoveryNone, PhaseInconsistent, ActionReviewDrift
			default:
				return RecoveryNone, PhaseInconsistent, ActionReviewEvidence
			}
		}
		if !domainsMatchFinalized(domains, inspection.Manifest.Domains) {
			return RecoveryNone, PhaseInconsistent, ActionReviewDomainState
		}
		if !inspection.FinalizedEvidence {
			return RecoveryNone, PhaseInconsistent, ActionReviewEvidence
		}
		if inspection.Live != consolidation.LiveGeositeCandidate {
			if inspection.Live == consolidation.LiveGeositeDrift {
				return RecoveryNone, PhaseInconsistent, ActionReviewDrift
			}
			return RecoveryNone, PhaseInconsistent, ActionReviewEvidence
		}
		return RecoveryCleanupCustom, PhaseCustomFinalizedCleanup, ""
	}
	return RecoveryNone, PhaseInconsistent, ActionReviewEvidence
}

func domainsMatchFinalized(snapshot domainstore.Snapshot, prepared []string) bool {
	return !snapshot.ConsolidationActive &&
		reflect.DeepEqual(snapshot.Desired, prepared) &&
		reflect.DeepEqual(snapshot.Baseline, prepared)
}

func result(phase Phase, recovery Recovery, operatorAction string) Result {
	blocked := phase != PhaseClean
	automatic := operatorAction == "" && (phase == PhaseClean || recovery != RecoveryNone)
	return Result{
		State: State{Phase: phase},
		Plan: Plan{
			Phase: phase, WritesBlocked: blocked, AutomaticRecovery: automatic,
			Recovery: recovery, OperatorAction: operatorAction,
		},
	}
}
