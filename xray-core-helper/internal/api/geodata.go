package api

import "strings"

type GeodataSettings struct {
	MaxAssetSizeMB int                   `json:"maxAssetSizeMB,omitempty"`
	Builder        string                `json:"builder,omitempty"`
	UserGeosite    string                `json:"userGeosite,omitempty"`
	UserGeoip      string                `json:"userGeoip,omitempty"`
	Schedule       *GeodataSchedule      `json:"schedule,omitempty"`
	Origins        []string              `json:"origins"`
	Geosite        GeodataSourceSettings `json:"geosite"`
	Geoip          GeodataSourceSettings `json:"geoip"`
}

type GeodataSchedule struct {
	Enabled bool   `json:"enabled"`
	Time    string `json:"time"`
}

type GeodataIssue struct {
	MaxAssetSizeMB int    `json:"maxAssetSizeMB,omitempty"`
	Process        string `json:"process"`
	File           string `json:"file,omitempty"`
	Stage          string `json:"stage"`
	Code           string `json:"code"`
	Message        string `json:"message"`
	At             int64  `json:"at"`
	Warning        bool   `json:"warning,omitempty"`
}

type GeodataFileResult struct {
	Process string `json:"process"`
	File    string `json:"file"`
	State   string `json:"state"`
}

type GeodataReport struct {
	EvidenceDirectory string              `json:"evidenceDirectory,omitempty"`
	Process           string              `json:"process,omitempty"`
	Stage             string              `json:"stage,omitempty"`
	Files             []GeodataFileResult `json:"files"`
	Issues            []GeodataIssue      `json:"issues"`
	Activation        *GeodataIssue       `json:"activation,omitempty"`
	Replaced          int                 `json:"replaced"`
	Restarted         bool                `json:"restarted"`
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
	Report             *GeodataReport `json:"report,omitempty"`
	ID                 string         `json:"id"`
	SettingsGeneration uint64         `json:"settingsGeneration"`
	State              string         `json:"state"`
	ResultCode         string         `json:"resultCode,omitempty"`
	ErrorCode          string         `json:"errorCode,omitempty"`
	CreatedAt          int64          `json:"createdAt"`
	UpdatedAt          int64          `json:"updatedAt"`
	FinishedAt         *int64         `json:"finishedAt,omitempty"`
}

type GeodataStatusResponse struct {
	LastSuccessfulAt *int64                   `json:"lastSuccessfulAt,omitempty"`
	NextScheduledAt  *int64                   `json:"nextScheduledAt,omitempty"`
	MaxAssetSizeMB   int                      `json:"maxAssetSizeMB,omitempty"`
	Settings         VersionedGeodataSettings `json:"settings"`
	Operation        *GeodataOperation        `json:"operation,omitempty"`
	Recovery         GeodataRecovery          `json:"recovery"`
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
