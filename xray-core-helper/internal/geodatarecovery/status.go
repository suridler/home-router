// Package geodatarecovery defines the read-only mutation-gate projection.
// File recovery belongs to the independent native safe-apply mechanism;
// geodata updates never start an automatic restart/rollback state machine.
package geodatarecovery

import "encoding/json"

type Phase uint8

const (
	PhaseClean Phase = iota
	PhaseInspectionFailed
	PhaseInconsistent
)

func (p Phase) String() string {
	switch p {
	case PhaseClean:
		return "clean"
	case PhaseInspectionFailed:
		return "inspection_failed"
	case PhaseInconsistent:
		return "inconsistent"
	}
	return "unknown"
}
func (p Phase) MarshalJSON() ([]byte, error) { return json.Marshal(p.String()) }

type Recovery uint8

const RecoveryNone Recovery = 0

func (r Recovery) String() string {
	if r == RecoveryNone {
		return "none"
	}
	return "unknown"
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
	Phase         Phase `json:"phase"`
	WritesBlocked bool  `json:"writesBlocked"`
}
type Result struct {
	State State `json:"state"`
	Plan  Plan  `json:"plan"`
}
type Status struct {
	Result           Result `json:"result"`
	Code             string `json:"code"`
	MutationsBlocked bool   `json:"mutationsBlocked"`
}

func UnavailableStatus() Status {
	return Status{Code: ActionRetryInspection, MutationsBlocked: true, Result: Result{State: State{Phase: PhaseInspectionFailed}, Plan: Plan{Phase: PhaseInspectionFailed, WritesBlocked: true}}}
}
