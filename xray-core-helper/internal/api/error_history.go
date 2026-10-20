package api

type ErrorEvent struct {
	ID         int64  `json:"id"`
	Time       string `json:"time,omitempty"`
	ObservedAt string `json:"observedAt"`
	Source     string `json:"source"`
	Message    string `json:"message"`
}

type ErrorHistoryResponse struct {
	Source          string       `json:"source"`
	Errors          []ErrorEvent `json:"errors"`
	Capacity        int          `json:"capacity"`
	CollectedAt     string       `json:"collectedAt,omitempty"`
	CollectionError string       `json:"collectionError,omitempty"`
	Truncated       bool         `json:"truncated"`
}
