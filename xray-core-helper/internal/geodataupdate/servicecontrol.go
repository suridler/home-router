package geodataupdate

import (
	"context"
	"errors"

	"home-router/xray-core-helper/internal/servicecontrol"
)

type ServiceControlRestarter struct {
	services ServiceController
}

func NewServiceControlRestarter(services ServiceController) (*ServiceControlRestarter, error) {
	if services == nil {
		return nil, errors.New("geodata service controller is required")
	}
	return &ServiceControlRestarter{services: services}, nil
}

func (restarter *ServiceControlRestarter) RestartWhileLocked(ctx context.Context) error {
	_, err := restarter.services.Execute(ctx, servicecontrol.UnitXray, servicecontrol.ActionRestart)
	return err
}

type RoutingServiceHealthVerifier struct {
	routing RoutingHealth
}

func NewRoutingServiceHealthVerifier(routing RoutingHealth) (*RoutingServiceHealthVerifier, error) {
	if routing == nil {
		return nil, errors.New("Xray routing health client is required")
	}
	return &RoutingServiceHealthVerifier{routing: routing}, nil
}

func (verifier *RoutingServiceHealthVerifier) VerifyActive(ctx context.Context) error {
	_, err := verifier.routing.ListRules(ctx)
	if err != nil {
		return err
	}
	return nil
}

type ServiceController interface {
	Execute(context.Context, servicecontrol.Unit, servicecontrol.Action) (servicecontrol.Result, error)
}
type RoutingHealth interface {
	ListRules(context.Context) ([]byte, error)
}
type RuntimeOverlay interface{ ApplyWhileLocked(context.Context) error }
