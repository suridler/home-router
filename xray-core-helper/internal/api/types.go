package api

import "time"

type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

type ReadinessCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Code   string `json:"code"`
}

type ReadinessResponse struct {
	Status string           `json:"status"`
	Checks []ReadinessCheck `json:"checks"`
}

type ErrorDetail struct {
	MaxAssetSizeMB int    `json:"maxAssetSizeMB,omitempty"`
	Code           string `json:"code"`
	Message        string `json:"message"`
}

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ConfigMetadata struct {
	Generation  uint64    `json:"generation"`
	ContentHash string    `json:"contentHash"`
	ModifiedAt  time.Time `json:"modifiedAt"`
}

type RuntimeResponse struct {
	Binary          string `json:"binary"`
	Config          string `json:"config"`
	API             string `json:"api"`
	AssetDir        string `json:"assetDir"`
	BinaryAvailable bool   `json:"binaryAvailable"`
	ConfigAvailable bool   `json:"configAvailable"`
	AssetDirPresent bool   `json:"assetDirPresent"`
}

// ExporterMetadataResponse is the complete low-frequency contract needed by
// xray-routing-exporter. It deliberately contains tags only, never outbound
// settings or other credential-bearing configuration.
type ExporterMetadataResponse struct {
	ConfigMetadata
	API          string   `json:"api"`
	InboundTags  []string `json:"inboundTags"`
	OutboundTags []string `json:"outboundTags"`
}

type StatusResponse struct {
	ConfigState    string          `json:"configState"`
	ConfigReadable bool            `json:"configReadable"`
	ConfigParsed   bool            `json:"configParsed"`
	ConfigDrift    bool            `json:"configDrift"`
	ConfigError    string          `json:"configError"`
	Runtime        RuntimeResponse `json:"runtime"`
	ConfigMetadata
}

type ConfigSummaryResponse struct {
	ConfigMetadata
	Sections []string `json:"sections"`
}

type InboundsResponse struct {
	ConfigMetadata
	Inbounds []InboundProjection `json:"inbounds"`
}

type InboundProjection struct {
	Tag      string `json:"tag"`
	Listen   string `json:"listen"`
	Port     any    `json:"port"`
	Protocol string `json:"protocol"`
}

type OutboundsResponse struct {
	ConfigMetadata
	Outbounds []OutboundProjection `json:"outbounds"`
}

type OutboundProjection struct {
	Tag      string `json:"tag"`
	Protocol string `json:"protocol"`
}

type RoutingResponse struct {
	ConfigMetadata
	DomainStrategy string                  `json:"domainStrategy"`
	Rules          []RoutingRuleProjection `json:"rules"`
	Balancers      []BalancerProjection    `json:"balancers"`
}

type RoutingRuleProjection struct {
	Index            int                `json:"index"`
	Tag              string             `json:"tag"`
	Type             string             `json:"type"`
	Description      string             `json:"description"`
	Protected        bool               `json:"protected"`
	ProtectionReason string             `json:"protectionReason"`
	OutboundTag      string             `json:"outboundTag"`
	BalancerTag      string             `json:"balancerTag"`
	Conditions       []RoutingCondition `json:"conditions"`
}

type RoutingCondition struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type BalancerProjection struct {
	Tag      string   `json:"tag"`
	Selector []string `json:"selector"`
	Strategy string   `json:"strategy"`
}

type LoggingResponse struct {
	ConfigMetadata
	Access   string `json:"access"`
	Error    string `json:"error"`
	Loglevel string `json:"loglevel"`
	DNSLog   bool   `json:"dnsLog"`
}

type LogTargetMode string

const (
	LogTargetDisabled LogTargetMode = "disabled"
	LogTargetJournal  LogTargetMode = "journal"
	LogTargetFile     LogTargetMode = "file"
)

type LogTarget struct {
	Mode LogTargetMode `json:"mode"`
}

type LogConfigurationRequest struct {
	Access   LogTarget           `json:"access"`
	Error    LogTarget           `json:"error"`
	Loglevel string              `json:"loglevel"`
	DNSLog   *bool               `json:"dnsLog"`
	Rotation *LogRotationRequest `json:"rotation,omitempty"`
}

type LogRotationRequest struct {
	Enabled bool `json:"enabled"`
}

type ValidationResponse struct {
	Status string `json:"status"`
}

type ServiceActionResponse struct {
	Unit   string `json:"unit"`
	Action string `json:"action"`
	State  string `json:"state,omitempty"`
}

type DiagnosticCheck struct {
	TemplateID string `json:"templateId,omitempty"`
	BindingID  string `json:"bindingId,omitempty"`
	Code       string `json:"code,omitempty"`
	Name       string `json:"name"`
	Status     string `json:"status"`
}

type DiagnosticsResponse struct {
	Checks []DiagnosticCheck `json:"checks"`
}

type LogReadResponse struct {
	Kind      string   `json:"kind"`
	Source    string   `json:"source"`
	Lines     []string `json:"lines"`
	BytesRead int      `json:"bytesRead"`
	ByteLimit int      `json:"byteLimit"`
	LineLimit int      `json:"lineLimit"`
	Truncated bool     `json:"truncated"`
}

type LogConfigurationMutationRequest struct {
	MutationRequest
	LogConfigurationRequest
	ConfirmRestrictedPaths bool `json:"confirmRestrictedPaths,omitempty"`
}

type GeodataResponse struct {
	ConfigMetadata
	AssetDir string        `json:"assetDir"`
	Files    []GeodataFile `json:"files"`
}

type GeodataFile struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Present    bool      `json:"present"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt,omitempty"`
}

type AdoptConfigRequest struct {
	ExpectedGeneration *uint64 `json:"expectedGeneration"`
}

type AdoptConfigResponse struct {
	Generation  uint64 `json:"generation"`
	ContentHash string `json:"contentHash"`
	Adopted     bool   `json:"adopted"`
}

type RuleConditions struct {
	InboundTag []string `json:"inboundTag"`
	Domain     []string `json:"domain"`
	IP         []string `json:"ip"`
	Port       []string `json:"port"`
	Network    []string `json:"network"`
	Protocol   []string `json:"protocol"`
}

type RuleInput struct {
	RuleTag     string         `json:"ruleTag"`
	Conditions  RuleConditions `json:"conditions"`
	OutboundTag string         `json:"outboundTag"`
	BalancerTag string         `json:"balancerTag"`
}

type MutationRequest struct {
	ExpectedGeneration *uint64 `json:"expectedGeneration"`
	Restart            bool    `json:"restart"`
}

type MutationResponse struct {
	Generation  uint64 `json:"generation"`
	ContentHash string `json:"contentHash"`
}

type VlessPreviewRequest struct {
	URI string `json:"uri"`
}

type VlessPreviewResponse struct {
	Address               string   `json:"address"`
	Port                  int      `json:"port"`
	Transport             string   `json:"transport"`
	Security              string   `json:"security"`
	Flow                  string   `json:"flow"`
	ServerName            string   `json:"serverName"`
	UnsupportedParameters []string `json:"unsupportedParameters"`
}

type AddVlessOutboundRequest struct {
	MutationRequest
	Tag string `json:"tag"`
	URI string `json:"uri"`
}

type UpdateVlessOutboundRequest struct {
	MutationRequest
	URI string `json:"uri"`
}

type AddRuleRequest struct {
	MutationRequest
	Rule     RuleInput `json:"rule"`
	Position int       `json:"position"`
}

type EditRuleRequest struct {
	MutationRequest
	Rule RuleInput `json:"rule"`
}

type MoveRuleRequest struct {
	MutationRequest
	Direction string `json:"direction"`
}

type CloneRuleRequest struct {
	MutationRequest
	Position int    `json:"position"`
	RuleTag  string `json:"ruleTag"`
}

type ProxyDomainsResponse struct {
	Desired             []string `json:"desired"`
	Baseline            []string `json:"baseline"`
	ConsolidationActive bool     `json:"consolidationActive"`
}

type AddProxyDomainRequest struct {
	Domain string `json:"domain"`
}

type ProxyDomainMutationResponse struct {
	Domain  string `json:"domain"`
	Applied bool   `json:"applied"`
}

type ApplyProxyDomainsResponse struct {
	Applied bool `json:"applied"`
}

// XrayOverviewResponse is a set of redacted projections of one JSONC document.
// Status remains available when the config cannot be read or parsed.
type XrayOverviewResponse struct {
	Status    StatusResponse    `json:"status"`
	Inbounds  InboundsResponse  `json:"inbounds"`
	Outbounds OutboundsResponse `json:"outbounds"`
	Routing   RoutingResponse   `json:"routing"`
	Logging   LoggingResponse   `json:"logging"`
}
