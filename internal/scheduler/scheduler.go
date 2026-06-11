// Package scheduler runs the daily sync-then-reconcile cycle on a
// recompute-next-fire timer (KTD7): each loop computes the next wall-clock fire
// time and sleeps via time.Timer, self-correcting for drift and DST. A startup
// missed-run check covers laptop-sleep gaps, and RunNow triggers an out-of-band
// cycle (R4).
package scheduler

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// ParseDailyAt parses "HH:MM" (24-hour) into hour and minute.
func ParseDailyAt(s string) (hour, min int, err error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid daily_at %q (want HH:MM)", s)
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour in %q", s)
	}
	min, err = strconv.Atoi(parts[1])
	if err != nil || min < 0 || min > 59 {
		return 0, 0, fmt.Errorf("invalid minute in %q", s)
	}
	return hour, min, nil
}

// NextFire returns the next occurrence of hour:min strictly after now, in now's
// location.
func NextFire(now time.Time, hour, min int) time.Time {
	n := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, now.Location())
	if !n.After(now) {
		n = n.Add(24 * time.Hour)
	}
	return n
}

// MissedToday reports whether today's scheduled run was missed: the scheduled
// time has passed and the last successful run predates it. Covers the case
// where the machine was asleep across the scheduled time (KTD7).
func MissedToday(now, lastRun time.Time, hour, min int) bool {
	today := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, now.Location())
	return !now.Before(today) && lastRun.Before(today)
}

// Scheduler fires job daily at the configured time, plus on demand.
type Scheduler struct {
	hour, min int
	job       func(context.Context)
	now       func() time.Time
	trigger   chan struct{}
}

// New builds a Scheduler for dailyAt ("HH:MM"). job is the cycle to run.
func New(dailyAt string, job func(context.Context)) (*Scheduler, error) {
	h, m, err := ParseDailyAt(dailyAt)
	if err != nil {
		return nil, err
	}
	return &Scheduler{
		hour:    h,
		min:     m,
		job:     job,
		now:     time.Now,
		trigger: make(chan struct{}, 1),
	}, nil
}

// RunNow requests an out-of-band cycle (non-blocking; coalesces if one is
// already pending).
func (s *Scheduler) RunNow() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Run blocks until ctx is cancelled, firing job daily and on RunNow. lastRun is
// the timestamp of the last successful cycle (zero if never), used for the
// startup missed-run check.
func (s *Scheduler) Run(ctx context.Context, lastRun time.Time) {
	if MissedToday(s.now(), lastRun, s.hour, s.min) {
		s.runJob(ctx)
	}
	for {
		now := s.now() // sample once so the timer interval can't go negative
		timer := time.NewTimer(NextFire(now, s.hour, s.min).Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.runJob(ctx)
		case <-s.trigger:
			timer.Stop()
			s.runJob(ctx)
		}
	}
}

// runJob runs the cycle with panic recovery so a single failing sync can never
// kill the scheduler loop and silently stop all future daily runs.
func (s *Scheduler) runJob(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("skillmanage: scheduled job panicked, loop continues: %v", r)
		}
	}()
	s.job(ctx)
}
