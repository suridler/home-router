package readiness

import (
	"strings"

	"home-router/xray-core-helper/internal/api"
)

func healthyTemplateStatus() TemplateStatus {
	hash := strings.Repeat("a", 64)
	return templatesFake{
		ready: true,
		response: api.TemplateStatusResponse{Templates: []api.TemplateStatus{{
			TemplateID: "xray-config", State: api.TemplateSynchronized,
			TemplateHash: hash, SystemHash: hash, Revision: strings.Repeat("b", 64),
			Summary: api.TemplateStatusSummary{TemplateAvailable: true, SystemAvailable: true},
		}}},
	}
}
