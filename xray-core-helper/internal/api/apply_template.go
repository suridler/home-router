package api

import "time"

type ApplyTemplateRequest struct {
	ExpectedGeneration *uint64 `json:"expectedGeneration"`
	InitializeBlock    bool    `json:"initializeBlock,omitempty"`
	ExpectedRevision   string  `json:"expectedRevision,omitempty"`
}

type ApplyTemplateResponse struct {
	TemplateID    string    `json:"templateId"`
	BackupID      string    `json:"backupId"`
	Generation    uint64    `json:"generation"`
	Applied       bool      `json:"applied"`
	TransactionID string    `json:"transactionId"`
	Deadline      time.Time `json:"deadline"`
	Phase         string    `json:"phase"`
	State         string    `json:"state"`
	EffectsStatus string    `json:"effectsStatus"`
}
