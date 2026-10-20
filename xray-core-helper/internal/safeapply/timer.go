package safeapply

import (
	"context"
	"errors"
	"time"

	"home-router/xray-core-helper/internal/commandrunner"
)

const rollbackTimerUnit = "xray-core-helper-rollback.timer"

type timerRunner interface {
	Run(context.Context, []string) error
}
type boundedTimerRunner struct{ runner *commandrunner.Runner }

func (runner boundedTimerRunner) Run(ctx context.Context, args []string) error {
	_, err := runner.runner.Run(ctx, args)
	return err
}

type SystemdTimer struct{ runner timerRunner }

func NewSystemdTimer() (*SystemdTimer, error) {
	runner, err := commandrunner.New("/usr/bin/systemctl", 2*time.Minute)
	if err != nil {
		return nil, err
	}
	return &SystemdTimer{runner: boundedTimerRunner{runner}}, nil
}
func newSystemdTimer(runner timerRunner) (*SystemdTimer, error) {
	if runner == nil {
		return nil, errors.New("timer runner is required")
	}
	return &SystemdTimer{runner: runner}, nil
}
func (timer *SystemdTimer) Arm(ctx context.Context) error {
	return timer.runner.Run(ctx, []string{"restart", rollbackTimerUnit})
}
func (timer *SystemdTimer) Cancel(ctx context.Context) error {
	return timer.runner.Run(ctx, []string{"stop", rollbackTimerUnit})
}
