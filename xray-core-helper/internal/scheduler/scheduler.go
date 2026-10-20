// Package scheduler runs bounded maintenance jobs inside the normal-mode daemon.
// Jobs run independently, but an individual job never overlaps itself.
package scheduler

import (
	"context"
	"sync"
	"time"
)

type Job struct {
	Name    string
	Next    func(time.Time) time.Time
	Timeout time.Duration
	Run     func(context.Context, time.Time) error
	// Pending preserves a daily deadline across daemon/host restarts. A first
	// installation starts with the next future deadline, without an immediate update.
	Pending  func(context.Context, time.Time) (time.Time, error)
	Complete func(context.Context, time.Time) error
}

// Every schedules the next invocation after the previous invocation finishes.
// Slow work cannot accumulate a backlog of ticks.
func Every(interval time.Duration) func(time.Time) time.Time {
	return func(now time.Time) time.Time { return now.Add(interval) }
}

// Daily uses local calendar dates rather than 24-hour intervals, including DST.
func Daily(hour, minute int) func(time.Time) time.Time {
	return func(now time.Time) time.Time {
		next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		if !next.After(now) {
			next = time.Date(now.Year(), now.Month(), now.Day()+1, hour, minute, 0, 0, now.Location())
		}
		return next
	}
}

// Start returns a join function. Cancel ctx before joining and before closing
// databases. Reports contain only fixed job names and health transitions; raw
// errors may contain secrets and are deliberately never sent to the reporter.
func Start(ctx context.Context, jobs []Job, report func(name string, failed bool)) func() {
	var group sync.WaitGroup
	for _, job := range jobs {
		group.Add(1)
		go func() {
			defer group.Done()
			run(ctx, job, report)
		}()
	}
	return group.Wait
}

func run(ctx context.Context, job Job, report func(string, bool)) {
	failed := false
	for ctx.Err() == nil {
		due := job.Next(time.Now())
		if job.Pending != nil {
			var err error
			due, err = job.Pending(ctx, due)
			if err != nil {
				if !failed && report != nil {
					report(job.Name, true)
				}
				failed = true
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Minute):
					continue
				}
			}
		}
		timer := time.NewTimer(time.Until(due))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		workCtx, cancel := context.WithTimeout(ctx, job.Timeout)
		err := job.Run(workCtx, due)
		cancel()
		if ctx.Err() != nil {
			return
		}
		persistFailed := false
		if job.Complete != nil {
			if completeErr := job.Complete(ctx, job.Next(time.Now())); completeErr != nil {
				err = completeErr
				persistFailed = true
			}
		}
		nowFailed := err != nil
		if nowFailed != failed && report != nil {
			report(job.Name, nowFailed)
		}
		failed = nowFailed
		if persistFailed {
			// A stale persisted deadline is already due. Back off instead of
			// repeatedly replaying the request in a tight loop after an I/O error.
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
			}
		}
	}
}
