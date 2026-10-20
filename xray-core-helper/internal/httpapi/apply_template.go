package httpapi

import (
	"context"
	"errors"
	"net/http"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/safeapply"
	"home-router/xray-core-helper/internal/templateapply"
	"home-router/xray-core-helper/internal/templatecatalog"
)

type ApplyTemplateService interface {
	ApplyForce(context.Context, templatecatalog.ID, uint64) (safeapply.Transaction, error)
	CancelReboot(context.Context, string) (safeapply.Transaction, bool, error)
	Begin(context.Context, templatecatalog.ID, uint64) (safeapply.Transaction, error)
	Pending(context.Context) (*safeapply.Transaction, error)
	Confirm(context.Context, string) (safeapply.Transaction, bool, error)
	Rollback(context.Context, string) (safeapply.Transaction, bool, error)
}

func applyTemplate(w http.ResponseWriter, r *http.Request, service ApplyTemplateService, id templatecatalog.ID) {
	request, ok := decodeUniqueBody[api.ApplyTemplateRequest](w, r)
	if !ok {
		return
	}
	if request.ExpectedGeneration == nil {
		writeAPIError(w, 400, "invalid_request", "expectedGeneration is required")
		return
	}
	if service == nil {
		writeAPIError(w, 503, "template_apply_unavailable", "template cannot be applied")
		return
	}
	ctx := r.Context()
	if request.InitializeBlock {
		if id != templatecatalog.Nftables || request.ExpectedRevision == "" {
			writeAPIError(w, 400, "invalid_request", "nft initialization requires an explicitly reviewed revision")
			return
		}
		ctx = templateapply.WithBlockInitialization(ctx, request.ExpectedRevision)
	} else if request.ExpectedRevision != "" {
		writeAPIError(w, 400, "invalid_request", "unexpected revision without initialization")
		return
	}
	var transaction safeapply.Transaction
	var err error
	if request.Force {
		transaction, err = service.ApplyForce(ctx, id, *request.ExpectedGeneration)
	} else {
		transaction, err = service.Begin(ctx, id, *request.ExpectedGeneration)
	}
	if err != nil {
		writeApplyTemplateError(w, err)
		return
	}
	effects := "unchecked"
	if id == templatecatalog.XrayConfig {
		effects = "xray_api_checked"
	}
	writeJSON(w, 200, api.ApplyTemplateResponse{TemplateID: string(transaction.TemplateID), BackupID: transaction.BackupID, Generation: transaction.Generation, Applied: true, TransactionID: transaction.ID, Deadline: transaction.Deadline, Phase: string(transaction.Phase), State: string(transaction.State), EffectsStatus: effects})
}
func safeApplyStatus(w http.ResponseWriter, r *http.Request, service ApplyTemplateService) {
	if service == nil {
		writeAPIError(w, 503, "safe_apply_unavailable", "safe apply is unavailable")
		return
	}
	pending, err := service.Pending(r.Context())
	if err != nil {
		writeApplyTemplateError(w, err)
		return
	}
	response := api.SafeApplyResponse{}
	if pending != nil {
		value := safeTransaction(*pending)
		response.Pending = &value
	}
	writeJSON(w, 200, response)
}
func resolveSafeApply(w http.ResponseWriter, r *http.Request, service ApplyTemplateService, id, action string) {
	if service == nil {
		writeAPIError(w, 503, "safe_apply_unavailable", "safe apply is unavailable")
		return
	}
	var transaction safeapply.Transaction
	var changed bool
	var err error
	if action == "cancel-reboot" {
		transaction, changed, err = service.CancelReboot(r.Context(), id)
	} else if action == "confirm" {
		transaction, changed, err = service.Confirm(r.Context(), id)
	} else {
		transaction, changed, err = service.Rollback(r.Context(), id)
	}
	if err != nil {
		writeApplyTemplateError(w, err)
		return
	}
	writeJSON(w, 200, api.SafeApplyResolutionResponse{Transaction: safeTransaction(transaction), Changed: changed})
}
func safeTransaction(value safeapply.Transaction) api.SafeApplyTransaction {
	return api.SafeApplyTransaction{TransactionID: value.ID, TemplateID: string(value.TemplateID), Deadline: value.Deadline, Phase: string(value.Phase), State: string(value.State), Generation: value.Generation, RebootRequired: value.State == safeapply.StatePending && value.Phase == safeapply.PhaseAwaitingReboot, RecoveryRequired: value.State == safeapply.StatePending && (value.Phase == safeapply.PhaseRollingBack || value.Phase == safeapply.PhasePrepared || value.Phase == safeapply.PhaseConfirming)}
}
func writeApplyTemplateError(w http.ResponseWriter, err error) {
	if errors.Is(err, safeapply.ErrBaseline) {
		writeAPIError(w, 409, "recovery_baseline_unavailable", "complete historical group baseline unavailable; preserve recovery evidence and restore the reviewed baseline manually; do not invent snapshots from current files")
		return
	}
	if errors.Is(err, safeapply.ErrRebootRequired) {
		writeAPIError(w, 409, "reboot_required", "system configuration files restored; runtime effects are not restored; manual reboot required")
		return
	}
	if errors.Is(err, safeapply.ErrPhase) {
		writeAPIError(w, 409, "safe_apply_phase_conflict", "operation is not allowed in this recovery phase")
		return
	}
	if errors.Is(err, safeapply.ErrBusy) {
		writeAPIError(w, 409, "safe_apply_pending", "another safe apply is pending")
		return
	}
	if errors.Is(err, safeapply.ErrStale) {
		writeAPIError(w, 409, "safe_apply_stale", "safe apply transaction is stale")
		return
	}
	if errors.Is(err, safeapply.ErrNotFound) {
		writeAPIError(w, 404, "safe_apply_not_found", "safe apply transaction was not found")
		return
	}
	var failure *templateapply.Error
	if !errors.As(err, &failure) {
		writeAPIError(w, 503, "template_apply_unavailable", "template cannot be applied")
		return
	}
	switch failure.Code {
	case templateapply.CodeUnknownTemplate:
		writeAPIError(w, 400, string(failure.Code), "template is unknown")
	case templateapply.CodeGeneration:
		writeAPIError(w, 409, string(failure.Code), "template state changed")
	case templateapply.CodeInitialize:
		writeAPIError(w, 409, string(failure.Code), "gateway markers absent; review existing configuration and explicitly initialize the block")
	case templateapply.CodeValidation, templateapply.CodeBindingInvalid, templateapply.CodeBindingUnresolved, templateapply.CodeBindingMismatch:
		writeAPIError(w, 422, string(failure.Code), "template cannot be applied")
	default:
		writeAPIError(w, 503, string(failure.Code), "template cannot be applied")
	}
}
