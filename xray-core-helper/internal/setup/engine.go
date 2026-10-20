package setup

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/xrayconfig"
	"net"
	"reflect"
	"regexp"
	"sort"
	"sync"
)

var (
	ErrInterfaceRequired = errors.New("network interface selection is required")
	ErrInvalidInterface  = errors.New("network interface is unavailable")
	ErrSetupNotRequired  = errors.New("setup is not required")
	ErrSetupBusy         = errors.New("setup is already running")
	ErrAddressRequired   = errors.New("IPv4 address selection is required")
)

type Artifact struct {
	TemplateID    string `json:"templateId"`
	Action        string `json:"action"`
	RuntimeAction string `json:"runtimeAction"`
}
type Plan struct {
	NativeFingerprints map[string]string   `json:"nativeFingerprints,omitempty"`
	ExistingXray       bool                `json:"existingXray,omitempty"`
	ReplaceXray        bool                `json:"replaceXray,omitempty"`
	Gateway            *xrayconfig.Gateway `json:"gateway,omitempty"`
	XrayCandidateHash  string              `json:"xrayCandidateHash,omitempty"`
	ConfigFingerprint  string              `json:"configFingerprint,omitempty"`
	PreserveXray       bool                `json:"preserveXray,omitempty"`
	xrayGID            int
	xraySource         []byte
	network            bootstrap.NetworkConfig
	Interface          string     `json:"interface"`
	ListenAddress      string     `json:"listenAddress"`
	LANNetwork         string     `json:"lanNetwork"`
	RoutingEnabled     bool       `json:"routingEnabled"`
	Artifacts          []Artifact `json:"artifacts"`
}
type Result struct {
	Status         string `json:"status"`
	Interface      string `json:"interface"`
	RoutingEnabled bool   `json:"routingEnabled"`
	OperationID    string `json:"operationId,omitempty"`
}
type SetupTransaction interface {
	Validate(context.Context) error
	Commit(context.Context) error
	Rollback(context.Context) error
	Complete(context.Context) error
}
type SetupStore interface {
	Prepare(context.Context, Plan) (SetupTransaction, error)
	Recover(context.Context) error
}
type XrayStarter interface{ Start(context.Context) error }
type Engine struct {
	mu        sync.Mutex
	probe     ConfigProbe
	discovery interface {
		Discover(context.Context) ([]Interface, error)
	}
	store    SetupStore
	xray     XrayStarter
	progress func(string) error
}

func NewEngine(probe ConfigProbe, discovery interface {
	Discover(context.Context) ([]Interface, error)
}, store SetupStore, xray XrayStarter) (*Engine, error) {
	if discovery == nil || store == nil || xray == nil {
		return nil, errors.New("setup dependencies are required")
	}
	return &Engine{probe: probe, discovery: discovery, store: store, xray: xray}, nil
}
func (e *Engine) Plan(ctx context.Context, name string) (Plan, error) {
	return e.PlanAddress(ctx, name, "")
}
func (e *Engine) PlanAddress(ctx context.Context, name, address string) (Plan, error) {
	return e.PlanWithConfig(ctx, name, address, false)
}
func (e *Engine) PlanWithConfig(ctx context.Context, name, address string, replace bool) (Plan, error) {
	if e.probe.RecoveryRequired {
		return Plan{}, ErrRecoveryRequired
	}
	if !e.probe.SetupRequired {
		return Plan{}, ErrSetupNotRequired
	}
	items, err := e.discovery.Discover(ctx)
	if err != nil {
		return Plan{}, err
	}
	plan, err := SelectionPlan(items, name, address)
	if err != nil {
		return Plan{}, err
	}
	if e.probe.State == ConfigExisting {
		plan.PreserveXray = true
		plan.ConfigFingerprint = e.probe.Fingerprint
		plan.Artifacts[0] = Artifact{"xray-config", "preserve", "unchanged"}
	}
	plan.ReplaceXray = replace
	plan.ExistingXray = e.probe.State == ConfigExisting
	if analyzer, ok := e.store.(interface {
		AnalyzeGateway(context.Context, Plan) (Plan, error)
	}); ok {
		return analyzer.AnalyzeGateway(ctx, plan)
	}
	return plan, err
}

// SelectionPlan is shared by daemon, CLI and installer; it never writes files.
func SelectionPlan(items []Interface, name, requestedAddress string) (Plan, error) {
	selected, err := selectInterface(items, name)
	if err != nil {
		return Plan{}, err
	}
	address, network, err := ipv4Selection(selected.Addresses, requestedAddress)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Interface: selected.Name, ListenAddress: address, LANNetwork: network, RoutingEnabled: false, Artifacts: []Artifact{{"xray-config", "install", "start_xray"}, {"nftables", "install", "disabled"}, {"tproxy-unit", "install", "disabled"}, {"sysctl", "install", "deferred"}}}, nil
}
func (e *Engine) Apply(ctx context.Context, plan Plan) (result Result, resultErr error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	expected, err := e.PlanWithConfig(ctx, plan.Interface, plan.ListenAddress, plan.ReplaceXray)
	if err != nil {
		return Result{}, err
	}
	if !equalPlan(plan, expected) {
		return Result{}, ErrInvalidInterface
	}
	if err = e.report("preflight"); err != nil {
		return Result{}, err
	}
	if preflight, ok := e.store.(interface {
		Preflight(context.Context, Plan) error
	}); ok {
		if err = preflight.Preflight(ctx, plan); err != nil {
			return Result{}, err
		}
	}
	if err = e.report("preparing"); err != nil {
		return Result{}, err
	}
	tx, err := e.store.Prepare(ctx, plan)
	if err != nil {
		return Result{}, err
	}
	done := false
	xrayTouched := false
	defer func() {
		if !done {
			if err := tx.Rollback(context.WithoutCancel(ctx)); err != nil {
				e.probe.RecoveryRequired = true
				resultErr = ErrRecoveryRequired
			} else if xrayTouched {
				if restorer, ok := e.xray.(interface {
					Restore(context.Context, bool) error
				}); ok {
					if restorer.Restore(context.WithoutCancel(ctx), plan.ExistingXray) != nil {
						e.probe.RecoveryRequired = true
						resultErr = ErrRecoveryRequired
					}
				}
			}
		}
	}()
	if err = e.report("validating"); err != nil {
		return Result{}, err
	}
	if err = tx.Validate(ctx); err != nil {
		return Result{}, err
	}
	if err = e.report("committing"); err != nil {
		return Result{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	if err = e.report("starting_xray"); err != nil {
		return Result{}, err
	}
	if plan.PreserveXray && plan.Gateway != nil {
		starter, ok := e.xray.(interface {
			Ensure(context.Context) (bool, error)
		})
		if !ok {
			return Result{}, &PreflightError{Code: "preflight_xray_gateway_not_ready"}
		}
		xrayTouched, err = starter.Ensure(ctx)
		if err != nil {
			return Result{}, &PreflightError{Code: "preflight_xray_gateway_not_ready"}
		}
	}
	if !plan.PreserveXray {
		xrayTouched = true
		if err = e.xray.Start(ctx); err != nil {
			return Result{}, err
		}
	}
	if plan.Gateway != nil {
		checker, ok := e.xray.(interface{ Ready(context.Context) error })
		if !ok {
			return Result{}, &PreflightError{Code: "preflight_xray_gateway_not_ready"}
		}
		if err = checker.Ready(ctx); err != nil {
			return Result{}, &PreflightError{Code: "preflight_xray_gateway_not_ready"}
		}
	}
	if err = e.report("finishing"); err != nil {
		return Result{}, err
	}
	if err = tx.Complete(ctx); err != nil {
		return Result{}, err
	}
	done = true
	e.probe = ConfigProbe{State: ConfigExisting}
	return Result{Status: "configured_direct", Interface: plan.Interface, RoutingEnabled: false}, nil
}
func (e *Engine) report(phase string) error {
	if e.progress != nil {
		return e.progress(phase)
	}
	return nil
}
func (e *Engine) Recover(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.Recover(ctx)
}

func (e *Engine) setProbe(probe ConfigProbe) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.probe = probe
}
func selectInterface(items []Interface, name string) (Interface, error) {
	if name != "" && !regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,32}$`).MatchString(name) {
		return Interface{}, ErrInvalidInterface
	}
	eligible := make([]Interface, 0)
	for _, item := range items {
		if name != "" && item.Name == name {
			if item.Excluded && item.Reason != "virtual" {
				return Interface{}, ErrInvalidInterface
			}
			return item, nil
		}
		if item.Excluded {
			continue
		}
		if _, _, err := ipv4Details(item.Addresses); errors.Is(err, ErrInvalidInterface) {
			continue
		}
		eligible = append(eligible, item)
	}
	if name != "" {
		return Interface{}, ErrInvalidInterface
	}
	if len(eligible) != 1 {
		return Interface{}, ErrInterfaceRequired
	}
	return eligible[0], nil
}
func ipv4Details(addresses []string) (string, string, error) {
	return ipv4Selection(addresses, "")
}
func ipv4Selection(addresses []string, requested string) (string, string, error) {
	values := append([]string(nil), addresses...)
	sort.Strings(values)
	choices := map[string]string{}
	for _, value := range values {
		ip, network, err := net.ParseCIDR(value)
		if err == nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast() {
			choices[ip.String()] = network.String()
		}
	}
	if requested != "" {
		if network, ok := choices[requested]; ok {
			return requested, network, nil
		}
		return "", "", ErrInvalidInterface
	}
	if len(choices) > 1 {
		return "", "", ErrAddressRequired
	}
	for address, network := range choices {
		return address, network, nil
	}
	return "", "", ErrInvalidInterface
}
func equalPlan(a, b Plan) bool {
	return reflect.DeepEqual(a, b)
}
