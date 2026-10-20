package api

import "strings"

type GeodataSettings struct {
	Origins []string              `json:"origins"`
	Geosite GeodataSourceSettings `json:"geosite"`
	Geoip   GeodataSourceSettings `json:"geoip"`
}

type GeodataSourceSettings struct {
	URL      string                  `json:"url"`
	Checksum GeodataChecksumSettings `json:"checksum"`
}

type GeodataChecksumSettings struct {
	URL       string `json:"url"`
	Algorithm string `json:"algorithm"`
}

type VersionedGeodataSettings struct {
	Generation uint64          `json:"generation"`
	Settings   GeodataSettings `json:"settings"`
}

type ReplaceGeodataSettingsRequest struct {
	ExpectedGeneration *uint64         `json:"expectedGeneration"`
	Settings           GeodataSettings `json:"settings"`
}

type GeodataRecovery struct {
	Phase            string `json:"phase"`
	Code             string `json:"code"`
	MutationsBlocked bool   `json:"mutationsBlocked"`
}

type GeodataOperation struct {
	ID                 string `json:"id"`
	SettingsGeneration uint64 `json:"settingsGeneration"`
	State              string `json:"state"`
	ResultCode         string `json:"resultCode,omitempty"`
	ErrorCode          string `json:"errorCode,omitempty"`
	CreatedAt          int64  `json:"createdAt"`
	UpdatedAt          int64  `json:"updatedAt"`
	FinishedAt         *int64 `json:"finishedAt,omitempty"`
}

type GeodataStatusResponse struct {
	MaxAssetSizeMB int                      `json:"maxAssetSizeMB,omitempty"`
	Settings       VersionedGeodataSettings `json:"settings"`
	Operation      *GeodataOperation        `json:"operation,omitempty"`
	Recovery       GeodataRecovery          `json:"recovery"`
}

type GeodataUpdateResponse struct {
	Operation GeodataOperation `json:"operation"`
}

func ValidIdempotencyKey(key string) bool {
	if len(key) < 1 || len(key) > 128 || strings.TrimSpace(key) != key {
		return false
	}
	for _, value := range []byte(key) {
		if (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') ||
			(value >= '0' && value <= '9') || strings.ContainsRune("-_.:", rune(value)) {
			continue
		}
		return false
	}
	return true
}
