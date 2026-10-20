package geodatarecovery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/consolidation"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/geodatainstall"
)

func TestClassifyPersistedPhasesAndCombinations(t *testing.T) {
	type testCase struct {
		name      string
		standard  geodatainstall.Inspection
		custom    consolidation.Inspection
		domains   domainstore.Snapshot
		phase     Phase
		recovery  Recovery
		operator  string
		blocked   bool
		automatic bool
	}
	cleanDB := domainstore.Snapshot{Desired: []string{"wanted.example"}, Baseline: []string{}}
	activeDB := domainstore.Snapshot{Desired: []string{"wanted.example"}, Baseline: []string{}, ConsolidationActive: true}
	finalizedDB := domainstore.Snapshot{
		Desired: []string{"wanted.example"}, Baseline: []string{"wanted.example"},
	}
	stdInstalled := standardManifest(geodatainstall.LiveAssetInstalled, geodatainstall.LiveAssetInstalled, false, false)
	stdPartial := standardManifest(geodatainstall.LiveAssetInstalled, geodatainstall.LiveAssetPrevious, true, true)
	stdFirstInstallPartial := standardManifest(geodatainstall.LiveAssetInstalled, geodatainstall.LiveAssetMissing, false, false)
	stdMissingRequiredPrevious := standardManifest(geodatainstall.LiveAssetInstalled, geodatainstall.LiveAssetMissing, true, true)
	stdDrift := standardManifest(geodatainstall.LiveAssetDrift, geodatainstall.LiveAssetInstalled, false, false)
	customCandidate := customManifest(consolidation.LiveGeositeCandidate, true, false)
	customPrevious := customManifest(consolidation.LiveGeositePrevious, true, false)
	customFinalized := customManifest(consolidation.LiveGeositeCandidate, true, true)
	tests := []testCase{
		{name: "custom pair partially replaced", custom: customManifest(consolidation.LiveAssetsMixed, true, false), domains: activeDB,
			phase: PhaseCustomPreparedMixed, recovery: RecoveryRollbackCustom, blocked: true, automatic: true},
		{name: "combined interrupted rollback of custom pair", standard: stdPartial, custom: customManifest(consolidation.LiveAssetsMixed, true, false), domains: activeDB,
			phase: PhaseCombinedRollback, recovery: RecoveryRollbackBoth, blocked: true, automatic: true},
		{name: "finalized DB cannot have mixed live pair", custom: customManifest(consolidation.LiveAssetsMixed, true, true), domains: finalizedDB,
			phase: PhaseInconsistent, operator: ActionReviewEvidence, blocked: true},
		{name: "installed standard with mixed custom pair requires review", standard: stdInstalled, custom: customManifest(consolidation.LiveAssetsMixed, true, false), domains: activeDB,
			phase: PhaseInconsistent, operator: ActionReviewEvidence, blocked: true},
		{name: "clean", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			custom: consolidation.Inspection{Phase: consolidation.InspectionClean}, domains: cleanDB,
			phase: PhaseClean, recovery: RecoveryNone, automatic: true},
		{name: "standard manifest absent cleanup only", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionCleanupOnly},
			custom: consolidation.Inspection{Phase: consolidation.InspectionClean}, domains: cleanDB,
			phase: PhaseStandardCleanup, recovery: RecoveryCleanupStandard, blocked: true, automatic: true},
		{name: "standard first install partial", standard: stdFirstInstallPartial,
			custom: consolidation.Inspection{Phase: consolidation.InspectionClean}, domains: cleanDB,
			phase: PhaseStandardPartial, recovery: RecoveryRollbackStandard, blocked: true, automatic: true},
		{name: "standard mixed previous and installed", standard: stdPartial,
			custom: consolidation.Inspection{Phase: consolidation.InspectionClean}, domains: cleanDB,
			phase: PhaseStandardPartial, recovery: RecoveryRollbackStandard, blocked: true, automatic: true},
		{name: "standard all candidate live", standard: stdInstalled,
			custom: consolidation.Inspection{Phase: consolidation.InspectionClean}, domains: cleanDB,
			phase: PhaseStandardInstalled, operator: ActionChooseDirection, blocked: true},
		{name: "standard missing required previous", standard: stdMissingRequiredPrevious,
			custom: consolidation.Inspection{Phase: consolidation.InspectionClean}, domains: cleanDB,
			phase: PhaseInconsistent, operator: ActionReviewEvidence, blocked: true},
		{name: "standard external drift", standard: stdDrift,
			custom: consolidation.Inspection{Phase: consolidation.InspectionClean}, domains: cleanDB,
			phase: PhaseInconsistent, operator: ActionReviewDrift, blocked: true},
		{name: "standard malformed evidence", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionInvalid},
			custom: consolidation.Inspection{Phase: consolidation.InspectionClean}, domains: cleanDB,
			phase: PhaseInconsistent, operator: ActionReviewEvidence, blocked: true},
		{name: "custom freeze before stage creation", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			custom: consolidation.Inspection{Phase: consolidation.InspectionClean}, domains: activeDB,
			phase: PhaseCustomFreezePartial, recovery: RecoveryClearCustomFreeze, blocked: true, automatic: true},
		{name: "custom freeze with partial files", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			custom: consolidation.Inspection{Phase: consolidation.InspectionPartial}, domains: activeDB,
			phase: PhaseCustomFreezePartial, recovery: RecoveryClearCustomFreeze, blocked: true, automatic: true},
		{name: "custom rollback cleanup only", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			custom: consolidation.Inspection{Phase: consolidation.InspectionPartial}, domains: cleanDB,
			phase: PhaseCustomPartialCleanup, recovery: RecoveryCleanupCustom, blocked: true, automatic: true},
		{name: "custom prepared candidate live", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			custom: customCandidate, domains: activeDB,
			phase: PhaseCustomPreparedCandidate, recovery: RecoveryRollbackCustom, blocked: true, automatic: true},
		{name: "custom prepared previous live", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			custom: customPrevious, domains: activeDB,
			phase: PhaseCustomPreparedPrevious, recovery: RecoveryRollbackCustom, blocked: true, automatic: true},
		{name: "custom freeze desired mismatch", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			custom: customCandidate, domains: domainstore.Snapshot{Desired: []string{"other.example"}, ConsolidationActive: true},
			phase: PhaseInconsistent, operator: ActionReviewDomainState, blocked: true},
		{name: "custom finalized cleanup", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			custom: customFinalized, domains: finalizedDB,
			phase: PhaseCustomFinalizedCleanup, recovery: RecoveryCleanupCustom, blocked: true, automatic: true},
		{name: "custom finalized baseline mismatch", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			custom: customFinalized, domains: cleanDB,
			phase: PhaseInconsistent, operator: ActionReviewDomainState, blocked: true},
		{name: "custom external drift", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			custom: customManifest(consolidation.LiveGeositeDrift, true, false), domains: activeDB,
			phase: PhaseInconsistent, operator: ActionReviewDrift, blocked: true},
		{name: "custom manifest missing staged evidence", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			custom:  consolidation.Inspection{Phase: consolidation.InspectionManifest, Manifest: &consolidation.Manifest{Domains: []string{"wanted.example"}}, Live: consolidation.LiveGeositeCandidate},
			domains: activeDB, phase: PhaseInconsistent, operator: ActionReviewEvidence, blocked: true},
		{name: "combined partial install and prepared candidate", standard: stdFirstInstallPartial,
			custom: customCandidate, domains: activeDB,
			phase: PhaseCombinedRollback, recovery: RecoveryRollbackBoth, blocked: true, automatic: true},
		{name: "combined installed candidate direction unknown", standard: stdInstalled,
			custom: customCandidate, domains: activeDB,
			phase: PhaseCombinedInstalled, operator: ActionChooseDirection, blocked: true},
		{name: "combined finalized evidence cleanup", standard: stdInstalled,
			custom: customFinalized, domains: finalizedDB,
			phase: PhaseCombinedCleanup, recovery: RecoveryCleanupBoth, blocked: true, automatic: true},
		{name: "combined cleanup-only standard and finalized custom", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionCleanupOnly},
			custom: customFinalized, domains: finalizedDB,
			phase: PhaseCombinedCleanup, recovery: RecoveryCleanupBoth, blocked: true, automatic: true},
		{name: "contradictory custom previous and standard manifest", standard: stdFirstInstallPartial,
			custom: customPrevious, domains: activeDB,
			phase: PhaseInconsistent, operator: ActionReviewEvidence, blocked: true},
		{name: "contradictory finalized DB and partial standard manifest", standard: stdFirstInstallPartial,
			custom: customFinalized, domains: finalizedDB,
			phase: PhaseInconsistent, operator: ActionReviewEvidence, blocked: true},
		{name: "active freeze with standard cleanup only", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionCleanupOnly},
			custom: consolidation.Inspection{Phase: consolidation.InspectionClean}, domains: activeDB,
			phase: PhaseInconsistent, operator: ActionReviewEvidence, blocked: true},
		{name: "custom unsafe or malformed entry", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			custom: consolidation.Inspection{Phase: consolidation.InspectionInvalid}, domains: activeDB,
			phase: PhaseInconsistent, operator: ActionReviewEvidence, blocked: true},
		{name: "transient candidate is not classifiable", standard: geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			custom: consolidation.Inspection{Phase: consolidation.InspectionClean, TransientPresent: true}, domains: cleanDB,
			phase: PhaseInconsistent, operator: ActionReviewEvidence, blocked: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classify(test.standard, test.custom, test.domains)
			wantRecovery := test.recovery
			if got.State.Phase != test.phase || got.Plan.Phase != test.phase ||
				got.Plan.Recovery != wantRecovery || got.Plan.OperatorAction != test.operator ||
				got.Plan.WritesBlocked != test.blocked || got.Plan.AutomaticRecovery != test.automatic {
				t.Fatalf("classify() = %#v; want phase=%q recovery=%q operator=%q blocked=%t automatic=%t",
					got, test.phase, wantRecovery, test.operator, test.blocked, test.automatic)
			}
		})
	}
}

func TestInspectIsDeterministicRedactedAndFailsClosed(t *testing.T) {
	err := errors.New("/private/path sha256:secret wanted.example")
	inspector, createErr := New(Options{
		Standard: standardSource{err: err},
		Custom:   customSource{},
		Domains:  domainSource{},
	})
	if createErr != nil {
		t.Fatal(createErr)
	}
	first := inspector.Inspect(context.Background())
	second := inspector.Inspect(context.Background())
	if first != second || first.Plan.Phase != PhaseInspectionFailed ||
		first.Plan.OperatorAction != ActionRetryInspection || !first.Plan.WritesBlocked ||
		first.Plan.AutomaticRecovery {
		t.Fatalf("failed inspection results differ or fail open: %#v / %#v", first, second)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"/private/path", "sha256:secret", "wanted.example"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public projection leaked %q: %s", secret, encoded)
		}
	}
}

func standardManifest(first, second geodatainstall.LiveAssetPhase, firstPrevious, secondPrevious bool) geodatainstall.Inspection {
	return geodatainstall.Inspection{
		Phase: geodatainstall.InspectionManifest,
		Manifest: &geodatainstall.Manifest{Assets: [2]geodatainstall.AssetRecord{
			{Name: "geosite.dat", PreviousExists: firstPrevious},
			{Name: "geoip.dat", PreviousExists: secondPrevious},
		}},
		Live: [2]geodatainstall.LiveAssetPhase{first, second},
	}
}

func customManifest(live consolidation.LiveGeositePhase, prepared, finalized bool) consolidation.Inspection {
	return consolidation.Inspection{
		Phase:            consolidation.InspectionManifest,
		Manifest:         &consolidation.Manifest{Domains: []string{"wanted.example"}},
		PreparedEvidence: prepared, FinalizedEvidence: finalized, Live: live,
	}
}

type standardSource struct {
	value geodatainstall.Inspection
	err   error
}

func (source standardSource) Inspect(context.Context) (geodatainstall.Inspection, error) {
	return source.value, source.err
}

type customSource struct {
	value consolidation.Inspection
	err   error
}

func (source customSource) Inspect(context.Context) (consolidation.Inspection, error) {
	return source.value, source.err
}

type domainSource struct {
	value domainstore.Snapshot
	err   error
}

func (source domainSource) Snapshot(context.Context) (domainstore.Snapshot, error) {
	return source.value, source.err
}
