package api

type BackupSettings struct {
	RetainPerTemplate int `json:"retainPerTemplate"`
}

type SystemSettingsResponse struct {
	Backup       BackupSettings       `json:"backup"`
	Connectivity ConnectivitySettings `json:"connectivity"`
}

type UpdateSystemSettingsRequest struct {
	Backup       BackupSettings        `json:"backup"`
	Connectivity *ConnectivitySettings `json:"connectivity,omitempty"`
}

type ConnectivitySettings struct {
	URL            string `json:"url"`
	ExpectedStatus int    `json:"expectedStatus"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}
