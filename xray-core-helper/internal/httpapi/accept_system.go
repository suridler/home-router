package httpapi

import (
	"context"
	"errors"
	"net/http"

	"home-router/xray-core-helper/internal/acceptsystem"
	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/templatecatalog"
)

type AcceptSystemService interface {
	Accept(context.Context, templatecatalog.ID, uint64, string) (acceptsystem.Result, error)
}

func acceptSystem(w http.ResponseWriter, r *http.Request, service AcceptSystemService, id templatecatalog.ID) {
	request, ok := decodeUniqueBody[api.AcceptSystemRequest](w, r)
	if !ok {
		return
	}
	if request.ExpectedGeneration == nil || request.ExpectedRevision == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "expectedGeneration and expectedRevision are required")
		return
	}
	if service == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "accept_system_unavailable", "system settings cannot be accepted")
		return
	}
	result, err := service.Accept(r.Context(), id, *request.ExpectedGeneration, request.ExpectedRevision)
	if err != nil {
		writeAcceptSystemError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.AcceptSystemResponse{TemplateID: result.TemplateID, Accepted: result.Accepted, BackupID: result.BackupID, Generation: result.Generation, Revision: result.Revision, State: result.State})
}
func writeAcceptSystemError(w http.ResponseWriter, err error) {
	var failure *acceptsystem.Error
	if !errors.As(err, &failure) {
		writeAPIError(w, http.StatusServiceUnavailable, "accept_system_unavailable", "system settings cannot be accepted")
		return
	}
	switch failure.Code {
	case acceptsystem.CodeUnknownTemplate:
		writeAPIError(w, http.StatusBadRequest, string(failure.Code), "template is unknown")
	case acceptsystem.CodeRevisionConflict, acceptsystem.CodeGenerationConflict:
		writeAPIError(w, http.StatusConflict, string(failure.Code), "template state changed")
	case acceptsystem.CodeSourceMissing, acceptsystem.CodeTargetMissing:
		writeAPIError(w, http.StatusConflict, string(failure.Code), "required native file is missing")
	case acceptsystem.CodeInvalidSystem, acceptsystem.CodeStateInvalid:
		writeAPIError(w, http.StatusUnprocessableEntity, string(failure.Code), "system settings cannot be accepted")
	default:
		writeAPIError(w, http.StatusServiceUnavailable, string(failure.Code), "system settings cannot be accepted")
	}
}
