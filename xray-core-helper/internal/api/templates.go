package api

// TemplateState is the comparison state of one fixed native template and its
// mapped system file.
type TemplateState string

const (
	TemplateSynchronized  TemplateState = "synchronized"
	TemplateDifferent     TemplateState = "different"
	TemplateMissing       TemplateState = "template_missing"
	TemplateSystemMissing TemplateState = "system_missing"
	TemplateInvalid       TemplateState = "invalid"
)

// TemplateSide identifies an invalid side without exposing its path or
// content.
type TemplateSide string

const (
	TemplateSideTemplate TemplateSide = "template"
	TemplateSideSystem   TemplateSide = "system"
	TemplateSideBoth     TemplateSide = "both"
)

// TemplateStatusSummary contains only safe availability and stable failure
// metadata. Code must be a redacted machine-readable value.
type TemplateStatusSummary struct {
	BlockInitializationRequired bool         `json:"blockInitializationRequired,omitempty"`
	TemplateAvailable           bool         `json:"templateAvailable"`
	SystemAvailable             bool         `json:"systemAvailable"`
	InvalidSide                 TemplateSide `json:"invalidSide,omitempty"`
	Code                        string       `json:"code,omitempty"`
}

// TemplateStatus is the transport-neutral comparison contract. Revision is a
// SHA-256 token derived from the ID, presence state and both content hashes;
// callers use it as expectedRevision for later synchronization mutations.
type TemplateStatus struct {
	TemplateID   string                `json:"templateId"`
	State        TemplateState         `json:"state"`
	TemplateHash string                `json:"templateHash,omitempty"`
	SystemHash   string                `json:"systemHash,omitempty"`
	Revision     string                `json:"revision"`
	Summary      TemplateStatusSummary `json:"summary"`
}

type TemplateStatusResponse struct {
	Templates []TemplateStatus `json:"templates"`
}
