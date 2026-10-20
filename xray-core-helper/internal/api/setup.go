package api

import "time"

type SetupOperation struct {
	NetworkInterface string    `json:"networkInterface,omitempty"`
	LANNetwork       string    `json:"lanNetwork,omitempty"`
	NetworkPrepared  bool      `json:"networkPrepared"`
	ID               string    `json:"id"`
	Status           string    `json:"status"`
	Phase            string    `json:"phase"`
	ErrorCode        string    `json:"errorCode,omitempty"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type SetupStatusResponse struct {
	ConfigFingerprint string          `json:"configFingerprint,omitempty"`
	Status            string          `json:"status"`
	ConfigState       string          `json:"configState"`
	SetupRequired     bool            `json:"setupRequired"`
	RequiresChoice    bool            `json:"requiresChoice"`
	RecoveryRequired  bool            `json:"recoveryRequired"`
	MutationsBlocked  bool            `json:"mutationsBlocked"`
	Operation         *SetupOperation `json:"operation,omitempty"`
}
type SetupInterface struct {
	Name         string   `json:"name"`
	Addresses    []string `json:"addresses"`
	DefaultRoute bool     `json:"defaultRoute"`
	Excluded     bool     `json:"excluded"`
	Reason       string   `json:"reason,omitempty"`
}
type SetupInterfacesResponse struct {
	Interfaces []SetupInterface `json:"interfaces"`
}

type SetupPlanRequest struct {
	ReplaceXray bool   `json:"replaceXray,omitempty"`
	Interface   string `json:"interface"`
	Address     string `json:"address,omitempty"`
}
type SetupPlanResponse struct {
	Plan any `json:"plan"`
}
type SetupApplyRequest struct {
	Interface      string `json:"interface"`
	ListenAddress  string `json:"listenAddress"`
	LANNetwork     string `json:"lanNetwork"`
	RoutingEnabled bool   `json:"routingEnabled"`
	Artifacts      []struct {
		TemplateID    string `json:"templateId"`
		Action        string `json:"action"`
		RuntimeAction string `json:"runtimeAction"`
	} `json:"artifacts"`
}
