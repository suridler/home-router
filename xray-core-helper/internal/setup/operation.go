package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type Operation struct {
	NetworkInterface string    `json:"networkInterface,omitempty"`
	LANNetwork       string    `json:"lanNetwork,omitempty"`
	NetworkPrepared  bool      `json:"networkPrepared,omitempty"`
	Version          int       `json:"version,omitempty"`
	ConfigPath       string    `json:"configPath,omitempty"`
	ID               string    `json:"id"`
	Status           string    `json:"status"`
	Phase            string    `json:"phase"`
	ErrorCode        string    `json:"errorCode,omitempty"`
	UpdatedAt        time.Time `json:"updatedAt"`
	HandoffPending   bool      `json:"handoffPending,omitempty"`
}
type operationStore interface {
	LoadOperation(context.Context) (*Operation, error)
	SaveOperation(context.Context, Operation) error
}

func (s *fileStore) LoadOperation(ctx context.Context) (*Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, _, err := readSafe(filepath.Join(s.root, "operation.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var op Operation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&op) != nil {
		return nil, ErrRecoveryRequired
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, ErrRecoveryRequired
	}
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(op.ID) || op.UpdatedAt.IsZero() {
		return nil, ErrRecoveryRequired
	}
	switch op.Status {
	case "setup_required", "applying", "configured_direct", "recovery_required":
	default:
		return nil, ErrRecoveryRequired
	}
	switch op.Phase {
	case "preflight", "preparing", "validating", "committing", "starting_xray", "finishing", "completed", "failed", "interrupted":
	default:
		return nil, ErrRecoveryRequired
	}
	if op.ErrorCode != "" && !knownOperationCode(op.ErrorCode) {
		return nil, ErrRecoveryRequired
	}
	return &op, nil
}
func (s *fileStore) SaveOperation(ctx context.Context, op Operation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := safePath(s.root); err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return s.replace(filepath.Join(s.root, "operation.json"), raw, 0600, os.Geteuid(), os.Getegid())
}
func knownOperationCode(code string) bool {
	switch code {
	case "preflight_xray_lifecycle_conflict", "preflight_network_must_be_inactive", "preflight_xray_gateway_not_ready", "preflight_xray_gateway_review_required", "setup_failed", "recovery_required", "setup_interrupted_rolled_back", "operation_status_unavailable", "interface_required", "address_required", "invalid_interface", "setup_not_required", "setup_busy", "preflight_binary_unavailable", "preflight_identity_unavailable", "preflight_xray_incompatible", "preflight_candidate_invalid", "preflight_asset_unavailable", "preflight_port_in_use", "preflight_web_port_in_use", "preflight_candidate_unavailable":
		return true
	}
	return false
}
func ErrorCode(err error) string {
	var preflight *PreflightError
	if errors.As(err, &preflight) && knownOperationCode(preflight.Code) {
		return preflight.Code
	}
	switch {
	case errors.Is(err, ErrRecoveryRequired):
		return "recovery_required"
	case errors.Is(err, ErrSetupBusy):
		return "setup_busy"
	case errors.Is(err, ErrSetupNotRequired):
		return "setup_not_required"
	case errors.Is(err, ErrInterfaceRequired):
		return "interface_required"
	case errors.Is(err, ErrAddressRequired):
		return "address_required"
	case errors.Is(err, ErrInvalidInterface):
		return "invalid_interface"
	}
	return "setup_failed"
}
