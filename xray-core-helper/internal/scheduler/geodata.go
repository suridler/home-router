package scheduler

import (
	"context"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
)

// ConfiguredGeodata observes TOML changes within a minute without restarting
// the daemon. Persisted daily deadlines still coalesce missed runs on startup.
func (s *Store) ConfiguredGeodata(socket string, load func(context.Context) (bootstrap.SchedulerConfig, error)) Job {
	request := GeodataJob(socket, 0, 0)
	return Job{Name: "geodata", Next: Every(time.Minute), Timeout: 20 * time.Minute,
		Run: func(ctx context.Context, now time.Time) error {
			cfg, err := load(ctx)
			if err != nil {
				return err
			}
			if !cfg.GeodataUpdateEnabled {
				return nil
			}
			hour, minute := 0, 0
			for i, value := range cfg.GeodataUpdateTime {
				if i < 2 {
					hour = hour*10 + int(value-'0')
				}
				if i > 2 {
					minute = minute*10 + int(value-'0')
				}
			}
			job := s.Persist(GeodataJob(socket, hour, minute), "geodata-"+cfg.GeodataUpdateTime)
			due, err := job.Pending(ctx, job.Next(now))
			if err != nil {
				return err
			}
			if due.After(now) {
				return nil
			}
			runErr := request.Run(ctx, due)
			if err := job.Complete(ctx, job.Next(now)); err != nil {
				return err
			}
			return runErr
		}}
}
