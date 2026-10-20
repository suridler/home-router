package api

import "errors"

var ErrInvalidTemplateStatus = errors.New("invalid template status")

// Validate enforces state-specific invariants without including status data in errors.
func (status TemplateStatus) Validate() error {
	if status.TemplateID == "" || !isSHA256(status.Revision) ||
		(status.TemplateHash != "" && !isSHA256(status.TemplateHash)) ||
		(status.SystemHash != "" && !isSHA256(status.SystemHash)) {
		return ErrInvalidTemplateStatus
	}
	clean := status.Summary.InvalidSide == "" && status.Summary.Code == ""
	switch status.State {
	case TemplateSynchronized:
		if !status.Summary.TemplateAvailable || !status.Summary.SystemAvailable || status.TemplateHash == "" || status.TemplateHash != status.SystemHash || !clean {
			return ErrInvalidTemplateStatus
		}
	case TemplateDifferent:
		if !status.Summary.TemplateAvailable || !status.Summary.SystemAvailable || status.TemplateHash == "" || status.SystemHash == "" || status.TemplateHash == status.SystemHash || !clean {
			return ErrInvalidTemplateStatus
		}
	case TemplateMissing:
		if status.Summary.TemplateAvailable || !status.Summary.SystemAvailable || status.TemplateHash != "" || status.SystemHash == "" || !clean {
			return ErrInvalidTemplateStatus
		}
	case TemplateSystemMissing:
		if !status.Summary.TemplateAvailable || status.Summary.SystemAvailable || status.TemplateHash == "" || status.SystemHash != "" || !clean {
			return ErrInvalidTemplateStatus
		}
	case TemplateInvalid:
		if status.Summary.Code == "" || !validInvalidSide(status.Summary) {
			return ErrInvalidTemplateStatus
		}
	default:
		return ErrInvalidTemplateStatus
	}
	return nil
}

func validInvalidSide(summary TemplateStatusSummary) bool {
	switch summary.InvalidSide {
	case TemplateSideTemplate, TemplateSideSystem, TemplateSideBoth:
		return true
	default:
		return false
	}
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
