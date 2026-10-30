package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

var (
	// ErrMonitorNameExists means a monitor with the same name already exists in
	// the workspace (unique constraint).
	ErrMonitorNameExists = errors.New("monitor name already exists")
)

// NextRunFunc computes the next scheduled time for a cron expression. The
// worker passes monitor.NextRun so the repository can advance next_run_at in
// the same transaction that claims a due monitor.
type NextRunFunc func(cron string, from time.Time) (time.Time, error)

// MonitorRepository persists monitors, their runs and the workspace snapshot
// needed to execute a request.
type MonitorRepository interface {
	CreateMonitor(ctx context.Context, monitor *entity.Monitor) error
	GetMonitor(ctx context.Context, id uuid.UUID) (*entity.Monitor, error)
	ListMonitors(ctx context.Context, workspaceID string) ([]entity.Monitor, error)
	UpdateMonitor(ctx context.Context, monitor *entity.Monitor, nextRunAt *time.Time) error
	DeleteMonitor(ctx context.Context, id uuid.UUID) error

	// RequestExists reports whether requestID is a live `request` entity in the
	// workspace.
	RequestExists(ctx context.Context, workspaceID, requestID string) (bool, error)

	ListRuns(ctx context.Context, monitorID uuid.UUID, limit int) ([]entity.MonitorRun, error)

	// ClaimDue atomically claims due monitors with FOR UPDATE SKIP LOCKED,
	// advances their next_run_at with next, and returns the claimed rows.
	ClaimDue(ctx context.Context, now time.Time, limit int, next NextRunFunc) ([]entity.Monitor, error)

	// RecordRun appends a finished run and returns the previous run (the most
	// recent completed run before this one) so callers can detect ok→fail
	// transitions. It also updates monitors.last_run_at.
	RecordRun(ctx context.Context, run *entity.MonitorRun) (*entity.MonitorRun, error)

	// LoadWorkspaceSnapshot rebuilds the app-shaped workspace document used by
	// the core runner.
	LoadWorkspaceSnapshot(ctx context.Context, workspaceID string) (*entity.WorkspaceSnapshot, error)
}
