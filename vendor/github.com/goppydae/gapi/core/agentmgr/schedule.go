package agentmgr

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Schedule yields fire times for a timer agent.
//
// Next returns the next instant the agent should fire, given the current
// time. A ZERO time means the schedule is exhausted and will never fire
// again; callers must check IsZero rather than treating the result as a
// duration. That is how one-shot schedules terminate.
type Schedule interface {
	Next(t time.Time) time.Time
}

// IntervalSchedule fires repeatedly, one interval apart.
type IntervalSchedule struct {
	interval time.Duration
}

func (s *IntervalSchedule) Next(t time.Time) time.Time {
	return t.Add(s.interval)
}

// OnceSchedule fires a single time, at a fixed instant, and never again.
//
// It is stateful by necessity: "have I already fired?" cannot be derived
// from the current time alone, because an elapse point in the past must
// still produce exactly one fire. systemd behaves the same way - a
// monotonic timer whose elapse point has passed triggers immediately on
// activation, once.
type OnceSchedule struct {
	at time.Time

	mu    sync.Mutex
	fired bool
}

func (s *OnceSchedule) Next(time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fired {
		return time.Time{}
	}
	s.fired = true
	return s.at
}

// CronSchedule fires on a cron expression.
type CronSchedule struct {
	schedule cron.Schedule
}

func (s *CronSchedule) Next(t time.Time) time.Time {
	return s.schedule.Next(t)
}

// ParseSchedule parses a schedule string, resolving relative forms
// against the current wall clock and the system boot time.
//
// Supported:
//   - OnUnitActiveSec=D - repeating, every D
//   - OnStartupSec=D    - ONCE, D after the timer starts
//   - OnBootSec=D       - ONCE, D after the system booted
//   - cron: "*/5 * * * *", and the descriptors @hourly/@daily/@weekly/@monthly
//   - raw Go duration: "5s", "1m30s" - repeating, treated as an interval
//
// The three systemd prefixes used to be aliases: parseSystemdSchedule
// stripped whichever one matched and returned an interval in every case,
// so OnBootSec=1m ran every minute instead of once (GAPI-DIV-036).
func ParseSchedule(s string) (Schedule, error) {
	return ParseScheduleAt(s, time.Now(), BootTime())
}

// ParseScheduleAt is ParseSchedule with time injected, so the
// boot-relative and startup-relative forms are testable without waiting
// on a real clock or a real boot.
func ParseScheduleAt(s string, now, boot time.Time) (Schedule, error) {
	s = strings.TrimSpace(s)

	if isSystemdSchedule(s) {
		return parseSystemdSchedule(s, now, boot)
	}

	// Raw duration, e.g. "5s".
	if dur, err := time.ParseDuration(s); err == nil {
		return &IntervalSchedule{interval: dur}, nil
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	schedule, err := parser.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("invalid schedule format: %w (expected systemd-style like 'OnUnitActiveSec=5s', duration like '5s', or cron expression like '*/5 * * * *')", err)
	}

	return &CronSchedule{schedule: schedule}, nil
}

// systemdPrefixes maps each accepted prefix to how its duration is
// anchored. Adding a prefix here without deciding its anchor is what
// produced the aliasing bug, so the table carries the decision.
var systemdPrefixes = []struct {
	prefix    string
	repeating bool
	// anchor picks the instant the duration is measured from. Ignored
	// when repeating.
	anchor func(now, boot time.Time) time.Time
}{
	{"OnUnitActiveSec=", true, nil},
	{"OnStartupSec=", false, func(now, _ time.Time) time.Time { return now }},
	{"OnBootSec=", false, func(_, boot time.Time) time.Time { return boot }},
}

func isSystemdSchedule(s string) bool {
	for _, p := range systemdPrefixes {
		if strings.HasPrefix(s, p.prefix) {
			return true
		}
	}
	return false
}

func parseSystemdSchedule(s string, now, boot time.Time) (Schedule, error) {
	for _, p := range systemdPrefixes {
		if !strings.HasPrefix(s, p.prefix) {
			continue
		}
		durStr := strings.TrimPrefix(s, p.prefix)
		d, err := time.ParseDuration(durStr)
		if err != nil {
			return nil, fmt.Errorf("invalid duration in schedule: %s (%w)", durStr, err)
		}
		if p.repeating {
			return &IntervalSchedule{interval: d}, nil
		}
		// An elapse point already in the past still fires, once, as soon
		// as the timer starts: the run loop clamps a negative delay to
		// zero. A timer declaring OnBootSec=5s on a host that has been up
		// for a week is late, not cancelled.
		return &OnceSchedule{at: p.anchor(now, boot).Add(d)}, nil
	}
	return nil, fmt.Errorf("invalid systemd-style schedule format: %q", s)
}
