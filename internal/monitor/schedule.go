package monitor

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// ParseCron validates a standard five-field cron expression (with the usual
// @hourly/@daily descriptors) and returns the schedule.
func ParseCron(spec string) (cron.Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("cron expression is required")
	}
	schedule, err := cron.ParseStandard(spec)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", spec, err)
	}
	return schedule, nil
}

// NextRun returns the next activation after from. Timestamps are normalized to
// UTC so they round-trip through Postgres without timezone surprises.
func NextRun(spec string, from time.Time) (time.Time, error) {
	schedule, err := ParseCron(spec)
	if err != nil {
		return time.Time{}, err
	}
	return schedule.Next(from.UTC()).UTC(), nil
}
