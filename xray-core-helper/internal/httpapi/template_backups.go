package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/fault"
	"home-router/xray-core-helper/internal/templatebackup"
	"home-router/xray-core-helper/internal/templatecatalog"
)

type TemplateBackupService interface {
	List(context.Context, templatecatalog.ID) ([]templatebackup.Record, uint64, error)
	Restore(context.Context, templatecatalog.ID, string, uint64) (templatebackup.Record, error)
}

func registerTemplateBackups(mux *http.ServeMux, backups TemplateBackupService, templates TemplateStatusService, accept AcceptSystemService, apply ApplyTemplateService) {
	mux.HandleFunc("/v1/safe-apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			safeApplyStatus(w, r, apply)
			return
		}
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
	mux.HandleFunc("/v1/safe-apply/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/safe-apply/"), "/")
		if len(parts) == 2 && r.Method == http.MethodPost && (parts[1] == "confirm" || parts[1] == "rollback" || parts[1] == "cancel-reboot") {
			resolveSafeApply(w, r, apply, parts[0], parts[1])
			return
		}
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
	mux.HandleFunc("/v1/templates/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/templates/"), "/")
		if len(parts) == 2 && parts[1] == "apply" && r.Method == http.MethodPost {
			applyTemplate(w, r, apply, templatecatalog.ID(parts[0]))
			return
		}
		if len(parts) == 2 && parts[1] == "accept-system" && r.Method == http.MethodPost {
			acceptSystem(w, r, accept, templatecatalog.ID(parts[0]))
			return
		}
		if len(parts) == 2 && parts[1] == "backups" && r.Method == http.MethodGet {
			listTemplateBackups(w, r, backups, templatecatalog.ID(parts[0]))
			return
		}
		if len(parts) == 4 && parts[1] == "backups" && parts[3] == "restore" && r.Method == http.MethodPost {
			restoreTemplateBackup(w, r, backups, templates, templatecatalog.ID(parts[0]), parts[2])
			return
		}
		writeError(w, &fault.Error{Code: fault.CodeNotFound, Message: "endpoint not found", HTTPStatus: http.StatusNotFound})
	})
}

func listTemplateBackups(w http.ResponseWriter, r *http.Request, service TemplateBackupService, id templatecatalog.ID) {
	if service == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "template_backups_unavailable", "template backups are unavailable")
		return
	}
	records, generation, err := service.List(r.Context(), id)
	if err != nil {
		writeBackupError(w, err)
		return
	}
	result := api.TemplateBackupsResponse{TemplateID: string(id), Generation: generation, Backups: make([]api.TemplateBackup, 0, len(records))}
	for _, record := range records {
		if record.State == templatebackup.StateCompleted && record.Generation > result.CompletedGeneration {
			result.CompletedGeneration = record.Generation
		}
		result.Backups = append(result.Backups, api.TemplateBackup{BackupID: record.BackupID, TemplateID: record.TemplateID, Side: string(record.Side), Hash: record.Hash, Mode: record.Mode, UID: record.UID, GID: record.GID, Generation: record.Generation, State: string(record.State), CreatedAt: record.CreatedAt})
	}
	writeJSON(w, http.StatusOK, result)
}

func restoreTemplateBackup(w http.ResponseWriter, r *http.Request, service TemplateBackupService, templates TemplateStatusService, id templatecatalog.ID, backupID string) {
	request, ok := decodeBody[api.RestoreTemplateBackupRequest](w, r)
	if !ok {
		return
	}
	if request.ExpectedGeneration == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "expectedGeneration is required")
		return
	}
	if service == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "template_backups_unavailable", "template backups are unavailable")
		return
	}
	record, err := service.Restore(r.Context(), id, backupID, *request.ExpectedGeneration)
	if err != nil {
		writeBackupError(w, err)
		return
	}
	_, generation, err := service.List(r.Context(), id)
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if templates != nil {
		templates.Refresh(r.Context())
	}
	writeJSON(w, http.StatusOK, api.RestoreTemplateBackupResponse{TemplateID: string(id), BackupID: record.BackupID, Generation: generation, Restored: true})
}

func writeBackupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, templatebackup.ErrSafeApply):
		writeAPIError(w, http.StatusConflict, "safe_apply_required", "system backup cannot bypass grouped recovery; review the backup as a working template and use confirmed apply")
	case errors.Is(err, templatebackup.ErrInvalid):
		writeAPIError(w, http.StatusBadRequest, "invalid_backup_request", "backup request is invalid")
	case errors.Is(err, templatebackup.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "backup_not_found", "backup was not found")
	case errors.Is(err, templatebackup.ErrConflict):
		writeAPIError(w, http.StatusConflict, "backup_generation_conflict", "backup generation changed")
	case errors.Is(err, templatebackup.ErrValidation):
		writeAPIError(w, http.StatusUnprocessableEntity, "backup_validation_failed", "backup failed native validation")
	default:
		writeAPIError(w, http.StatusServiceUnavailable, "template_backups_unavailable", "template backups are unavailable")
	}
}
