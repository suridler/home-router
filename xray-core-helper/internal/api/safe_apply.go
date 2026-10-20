package api

import "time"

type SafeApplyResponse struct {
	Pending *SafeApplyTransaction `json:"pending"`
}
type SafeApplyTransaction struct {
	TransactionID    string    `json:"transactionId"`
	TemplateID       string    `json:"templateId"`
	Deadline         time.Time `json:"deadline"`
	Phase            string    `json:"phase"`
	State            string    `json:"state"`
	Generation       uint64    `json:"generation"`
	RebootRequired   bool      `json:"rebootRequired"`
	RecoveryRequired bool      `json:"recoveryRequired"`
}
type SafeApplyResolutionResponse struct {
	Transaction SafeApplyTransaction `json:"transaction"`
	Changed     bool                 `json:"changed"`
}
