package agentmgr

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Schedule represents a timer schedule
type Schedule interface {
	Next(t time.Time) time.Time
}

// IntervalSchedule represents a simple interval-based schedule
type IntervalSchedule struct {
	interval time.Duration
}

func (s *IntervalSchedule) Next(t time.Time) time.Time {
	return t.Add(s.interval)
}

// CronSchedule represents a cron-based schedule
type CronSchedule struct {
	schedule cron.Schedule
}

func (s *CronSchedule) Next(t time.Time) time.Time {
	return s.schedule.Next(t)
}

// ParseSchedule parses a schedule string into a Schedule
// Supports:
// - Systemd-style: OnUnitActiveSec=5s, OnBootSec=30s, OnStartupSec=1m
// - Cron expressions: */5 * * * * (every 5 minutes)
// - Named schedules: @hourly, @daily, @weekly, @monthly
// - Raw durations: 5s, 1m, 1h (treated as intervals)
func ParseSchedule(s string) (Schedule, error) {
	s = strings.TrimSpace(s)

	// Try systemd-style first
	if strings.HasPrefix(s, "OnUnitActiveSec=") ||
		strings.HasPrefix(s, "OnBootSec=") ||
		strings.HasPrefix(s, "OnStartupSec=") {
		return parseSystemdSchedule(s)
	}

	// Try raw duration (e.g., "5s", "1m")
	if dur, err := time.ParseDuration(s); err == nil {
		return &IntervalSchedule{interval: dur}, nil
	}

	// Try cron syntax (including named schedules like @hourly)
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	schedule, err := parser.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("invalid schedule format: %w (expected systemd-style like 'OnUnitActiveSec=5s', duration like '5s', or cron expression like '*/5 * * * *')", err)
	}

	return &CronSchedule{schedule: schedule}, nil
}

// parseSystemdSchedule parses systemd-style timer schedules
func parseSystemdSchedule(s string) (Schedule, error) {
	validPrefixes := []string{"OnUnitActiveSec=", "OnBootSec=", "OnStartupSec="}

	for _, prefix := range validPrefixes {
		if strings.HasPrefix(s, prefix) {
			durStr := strings.TrimPrefix(s, prefix)
			interval, err := time.ParseDuration(durStr)
			if err != nil {
				return nil, fmt.Errorf("invalid duration in schedule: %s (%w)", durStr, err)
			}
			return &IntervalSchedule{interval: interval}, nil
		}
	}

	// Fallback: try parsing as raw duration
	if interval, err := time.ParseDuration(s); err == nil {
		return &IntervalSchedule{interval: interval}, nil
	}

	return nil, fmt.Errorf("invalid systemd-style schedule format")
}
