package entity

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Monitor is a scheduled request. It references a synced `request` entity by
// its opaque workspace-local id, not a UUID.
type Monitor struct {
	ID           uuid.UUID  `json:"id"`
	WorkspaceID  string     `json:"workspaceId"`
	Name         string     `json:"name"`
	RequestID    string     `json:"requestId"`
	Cron         string     `json:"cron"`
	Enabled      bool       `json:"enabled"`
	AlertWebhook *string    `json:"alertWebhook,omitempty"`
	NextRunAt    time.Time  `json:"nextRunAt"`
	LastRunAt    *time.Time `json:"lastRunAt,omitempty"`
	CreatedBy    uuid.UUID  `json:"createdBy"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// MonitorRun is one execution of a monitor. StatusCode/duration/assertion
// failures mirror the core runner result; Error is empty on success.
type MonitorRun struct {
	ID                uuid.UUID  `json:"id"`
	MonitorID         uuid.UUID  `json:"monitorId"`
	StartedAt         time.Time  `json:"startedAt"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
	OK                bool       `json:"ok"`
	StatusCode        int        `json:"statusCode"`
	DurationMS        int        `json:"durationMs"`
	AssertionFailures int        `json:"assertionFailures"`
	Error             *string    `json:"error,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
}

// WorkspaceSnapshot is the app-shaped workspace document handed to the core
// CLI. Collections are nested trees; environments/globals use the same shapes
// the desktop app persists.
type WorkspaceSnapshot struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Collections  []json.RawMessage `json:"collections"`
	Environments []json.RawMessage `json:"environments"`
	Globals      json.RawMessage   `json:"globals"`
	ActiveEnvID  *string           `json:"activeEnvId"`
}
