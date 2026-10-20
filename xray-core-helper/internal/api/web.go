package api

// WebSettingsResponse contains no bootstrap token or generated environment.
type WebSettingsResponse struct {
	ExporterURL    string `json:"exporterURL"`
	Enabled        bool   `json:"enabled"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	ControlEnabled bool   `json:"controlEnabled"`
}
