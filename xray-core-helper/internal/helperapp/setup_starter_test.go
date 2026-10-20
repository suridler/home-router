package helperapp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"home-router/xray-core-helper/internal/servicecontrol"
)

type setupServiceControl struct {
	state                 servicecontrol.State
	statusErr, restartErr error
	actions               []servicecontrol.Action
}

func (c *setupServiceControl) Execute(_ context.Context, unit servicecontrol.Unit, action servicecontrol.Action) (servicecontrol.Result, error) {
	if unit != servicecontrol.UnitXray {
		panic("unexpected unit")
	}
	c.actions = append(c.actions, action)
	if action == servicecontrol.ActionStatus {
		return servicecontrol.Result{State: c.state}, c.statusErr
	}
	if action == servicecontrol.ActionRestart {
		return servicecontrol.Result{}, c.restartErr
	}
	panic("unexpected service action")
}

func TestSetupReadyDependsOnlyOnXrayServiceStatus(t *testing.T) {
	for _, state := range []servicecontrol.State{servicecontrol.StateActive, servicecontrol.StateInactive, servicecontrol.StateFailed, servicecontrol.StateActivating} {
		t.Run(string(state), func(t *testing.T) {
			c := &setupServiceControl{state: state}
			err := (setupXrayStarter{services: c}).Ready(context.Background())
			if (err == nil) != (state == servicecontrol.StateActive) {
				t.Fatal(state, err)
			}
			if !reflect.DeepEqual(c.actions, []servicecontrol.Action{servicecontrol.ActionStatus}) {
				t.Fatal(c.actions)
			}
		})
	}
}

func TestSetupEnsureRestartsOnlyKnownNonactiveService(t *testing.T) {
	statusFailure := errors.New("status failure")
	restartFailure := errors.New("restart failure")
	for _, tc := range []struct {
		name                  string
		state                 servicecontrol.State
		statusErr, restartErr error
		wantRestart           bool
		wantErr               error
	}{
		{name: "active", state: servicecontrol.StateActive},
		{name: "inactive", state: servicecontrol.StateInactive, wantRestart: true},
		{name: "failed", state: servicecontrol.StateFailed, wantRestart: true},
		{name: "unknown status", statusErr: statusFailure, wantErr: statusFailure},
		{name: "restart failure", state: servicecontrol.StateFailed, restartErr: restartFailure, wantRestart: true, wantErr: restartFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &setupServiceControl{state: tc.state, statusErr: tc.statusErr, restartErr: tc.restartErr}
			restarted, err := (setupXrayStarter{services: c}).Ensure(context.Background())
			if restarted != tc.wantRestart || !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, %v", restarted, err)
			}
			actions := []servicecontrol.Action{servicecontrol.ActionStatus}
			if tc.wantRestart {
				actions = append(actions, servicecontrol.ActionRestart)
			}
			if !reflect.DeepEqual(c.actions, actions) {
				t.Fatal(c.actions)
			}
		})
	}
	c := &setupServiceControl{statusErr: statusFailure}
	if err := (setupXrayStarter{services: c}).Ready(context.Background()); !errors.Is(err, statusFailure) {
		t.Fatal(err)
	}
}
