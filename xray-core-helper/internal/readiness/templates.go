package readiness

import "home-router/xray-core-helper/internal/api"

func (service *Service) checkTemplates() api.ReadinessCheck {
	if service == nil || service.templates == nil {
		return degradedCheck("templates", "template_status_unavailable")
	}
	status, ok := service.templates.Current()
	if !ok || len(status.Templates) == 0 {
		return degradedCheck("templates", "template_status_unavailable")
	}
	different := false
	incomplete := false
	for _, item := range status.Templates {
		if item.State == api.TemplateInvalid {
			return degradedCheck("templates", "templates_invalid")
		}
		if item.State == api.TemplateDifferent {
			different = true
		} else if item.State != api.TemplateSynchronized {
			incomplete = true
		}
	}
	if incomplete {
		return degradedCheck("templates", "templates_different")
	}
	if different {
		return readyCheck("templates", "templates_available")
	}
	return readyCheck("templates", "templates_synchronized")
}
