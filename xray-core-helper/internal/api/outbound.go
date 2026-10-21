package api

import "encoding/json"

// Outbound content uses native Xray JSON, not a parallel helper protocol model.
type OutboundImportRequest struct {
	URI string `json:"uri"`
}
type OutboundImportResponse struct {
	Outbound json.RawMessage `json:"outbound"`
	Warnings []string        `json:"warnings"`
}
type OutboundWriteRequest struct {
	MutationRequest
	Outbound           json.RawMessage `json:"outbound"`
	ConfirmReplacement bool            `json:"confirmReplacement,omitempty"`
}
type OutboundEditResponse struct {
	ConfigMetadata
	Outbound json.RawMessage `json:"outbound"`
}
type OutboundReferencesResponse struct {
	ConfigMetadata
	Tag        string   `json:"tag"`
	Protected  bool     `json:"protected"`
	References []string `json:"references"`
}
