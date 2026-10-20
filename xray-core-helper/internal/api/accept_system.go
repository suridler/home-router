package api

type AcceptSystemRequest struct {
	ExpectedGeneration *uint64 `json:"expectedGeneration"`
	ExpectedRevision   string  `json:"expectedRevision"`
}

type AcceptSystemResponse struct {
	TemplateID string        `json:"templateId"`
	Accepted   bool          `json:"accepted"`
	BackupID   string        `json:"backupId,omitempty"`
	Generation uint64        `json:"generation"`
	Revision   string        `json:"revision"`
	State      TemplateState `json:"state"`
}
