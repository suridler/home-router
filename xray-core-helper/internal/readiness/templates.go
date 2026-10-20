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
	for _, item := range status.Templates {
		if item.State == api.TemplateInvalid {
			return degradedCheck("templates", "templates_invalid")
		}
		if item.State != api.TemplateSynchronized {
			different = true
		}
	}
	if different {
		return degradedCheck("templates", "templates_different")
	}
	return readyCheck("templates", "templates_synchronized")
}
