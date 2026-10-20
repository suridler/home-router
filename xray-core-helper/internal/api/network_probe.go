package api

import "time"

type NetworkProbeRequest struct {
	Stage         string `json:"stage"`
	TransactionID string `json:"transactionId,omitempty"`
}

type NetworkProbeCheck struct {
	Status     string `json:"status"`
	Code       string `json:"code"`
	DurationMS int64  `json:"durationMs"`
}

type NetworkProbeResult struct {
	Stage          string            `json:"stage"`
	TransactionID  string            `json:"transactionId,omitempty"`
	CheckedAt      time.Time         `json:"checkedAt"`
	URL            string            `json:"url"`
	ExpectedStatus int               `json:"expectedStatus"`
	HTTPStatus     int               `json:"httpStatus"`
	RemoteIP       string            `json:"remoteIP,omitempty"`
	Gateway        string            `json:"gateway,omitempty"`
	Interface      string            `json:"interface,omitempty"`
	DNS            NetworkProbeCheck `json:"dns"`
	GatewayPing    NetworkProbeCheck `json:"gatewayPing"`
	HTTPS          NetworkProbeCheck `json:"https"`
	DurationMS     int64             `json:"durationMs"`
	Reachable      bool              `json:"reachable"`
	LANForwarding  string            `json:"lanForwarding"`
}

type NetworkProbeHistory struct {
	Before *NetworkProbeResult `json:"before,omitempty"`
	After  *NetworkProbeResult `json:"after,omitempty"`
	Latest *NetworkProbeResult `json:"latest,omitempty"`
}
