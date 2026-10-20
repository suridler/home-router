package api

import "time"

type TemplateBackup struct {
	BackupID   string    `json:"backupId"`
	TemplateID string    `json:"templateId"`
	Side       string    `json:"side"`
	Hash       string    `json:"hash"`
	Mode       uint32    `json:"mode"`
	UID        uint32    `json:"uid"`
	GID        uint32    `json:"gid"`
	Generation uint64    `json:"generation"`
	State      string    `json:"state"`
	CreatedAt  time.Time `json:"createdAt"`
}

type TemplateBackupsResponse struct {
	TemplateID          string           `json:"templateId"`
	Generation          uint64           `json:"generation"`
	CompletedGeneration uint64           `json:"completedGeneration"`
	Backups             []TemplateBackup `json:"backups"`
}

type RestoreTemplateBackupRequest struct {
	ExpectedGeneration *uint64 `json:"expectedGeneration"`
}

type RestoreTemplateBackupResponse struct {
	TemplateID string `json:"templateId"`
	BackupID   string `json:"backupId"`
	Generation uint64 `json:"generation"`
	Restored   bool   `json:"restored"`
}
