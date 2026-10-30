package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

// monitorRepository stores scheduled monitors and the workspace snapshot the
// core runner consumes. Claiming uses FOR UPDATE SKIP LOCKED so multiple
// workers can share the queue safely.
type monitorRepository struct {
	db *gorm.DB
}

// NewMonitorRepository returns the Postgres-backed monitor repository.
func NewMonitorRepository(db *gorm.DB) domainrepo.MonitorRepository {
	return &monitorRepository{db: db}
}

func (r *monitorRepository) CreateMonitor(ctx context.Context, monitor *entity.Monitor) error {
	now := time.Now().UTC()
	monitor.CreatedAt = now
	monitor.UpdatedAt = now

	err := r.db.WithContext(ctx).Exec(
		`INSERT INTO monitors
		   (id, workspace_id, name, request_id, cron, enabled, alert_webhook,
		    next_run_at, last_run_at, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		monitor.ID, monitor.WorkspaceID, monitor.Name, monitor.RequestID, monitor.Cron,
		monitor.Enabled, monitor.AlertWebhook, monitor.NextRunAt, monitor.LastRunAt,
		monitor.CreatedBy, now, now,
	).Error
	if err != nil {
		if isUniqueViolation(err) {
			return domainrepo.ErrMonitorNameExists
		}
		return fmt.Errorf("insert monitor: %w", err)
	}
	return nil
}

func (r *monitorRepository) GetMonitor(ctx context.Context, id uuid.UUID) (*entity.Monitor, error) {
	var row monitorRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, workspace_id, name, request_id, cron, enabled, alert_webhook,
		        next_run_at, last_run_at, created_by, created_at, updated_at
		   FROM monitors WHERE id = ?`,
		id,
	).Scan(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get monitor: %w", err)
	}
	if row.ID == uuid.Nil {
		return nil, gorm.ErrRecordNotFound
	}
	return row.toEntity(), nil
}

func (r *monitorRepository) ListMonitors(ctx context.Context, workspaceID string) ([]entity.Monitor, error) {
	var rows []monitorRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, workspace_id, name, request_id, cron, enabled, alert_webhook,
		        next_run_at, last_run_at, created_by, created_at, updated_at
		   FROM monitors WHERE workspace_id = ?
		  ORDER BY created_at ASC, name ASC`,
		workspaceID,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list monitors: %w", err)
	}
	out := make([]entity.Monitor, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row.toEntity())
	}
	return out, nil
}

func (r *monitorRepository) UpdateMonitor(ctx context.Context, monitor *entity.Monitor, nextRunAt *time.Time) error {
	now := time.Now().UTC()
	monitor.UpdatedAt = now

	res := r.db.WithContext(ctx).Exec(
		`UPDATE monitors
		    SET name = ?, request_id = ?, cron = ?, enabled = ?, alert_webhook = ?,
		        next_run_at = COALESCE(?, next_run_at), updated_at = ?
		  WHERE id = ?`,
		monitor.Name, monitor.RequestID, monitor.Cron, monitor.Enabled,
		monitor.AlertWebhook, nextRunAt, now, monitor.ID,
	)
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			return domainrepo.ErrMonitorNameExists
		}
		return fmt.Errorf("update monitor: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *monitorRepository) DeleteMonitor(ctx context.Context, id uuid.UUID) error {
	res := r.db.WithContext(ctx).Exec(`DELETE FROM monitors WHERE id = ?`, id)
	if res.Error != nil {
		return fmt.Errorf("delete monitor: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *monitorRepository) RequestExists(ctx context.Context, workspaceID, requestID string) (bool, error) {
	var exists bool
	err := r.db.WithContext(ctx).Raw(
		`SELECT EXISTS (
		    SELECT 1 FROM entities
		     WHERE workspace_id = ? AND id = ? AND kind = 'request' AND deleted_at IS NULL
		 )`,
		workspaceID, requestID,
	).Scan(&exists).Error
	if err != nil {
		return false, fmt.Errorf("check request entity: %w", err)
	}
	return exists, nil
}

func (r *monitorRepository) ListRuns(ctx context.Context, monitorID uuid.UUID, limit int) ([]entity.MonitorRun, error) {
	var rows []monitorRunRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, monitor_id, started_at, finished_at, ok, status_code, duration_ms,
		        assertion_failures, error, created_at
		   FROM monitor_runs
		  WHERE monitor_id = ?
		  ORDER BY started_at DESC, created_at DESC
		  LIMIT ?`,
		monitorID, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list monitor runs: %w", err)
	}
	out := make([]entity.MonitorRun, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row.toEntity())
	}
	return out, nil
}

func (r *monitorRepository) ClaimDue(
	ctx context.Context,
	now time.Time,
	limit int,
	next domainrepo.NextRunFunc,
) ([]entity.Monitor, error) {
	claimed := make([]entity.Monitor, 0, limit)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []monitorRow
		err := tx.Raw(
			`SELECT m.id, m.workspace_id, m.name, m.request_id, m.cron, m.enabled,
			        m.alert_webhook, m.next_run_at, m.last_run_at, m.created_by,
			        m.created_at, m.updated_at
			   FROM monitors m
			  WHERE m.enabled
			    AND m.next_run_at <= ?
			    AND EXISTS (
			        SELECT 1 FROM workspaces w
			         WHERE w.id = m.workspace_id AND w.deleted_at IS NULL
			    )
			  ORDER BY m.next_run_at ASC, m.id ASC
			  FOR UPDATE SKIP LOCKED
			  LIMIT ?`,
			now, limit,
		).Scan(&rows).Error
		if err != nil {
			return fmt.Errorf("claim due monitors: %w", err)
		}

		for _, row := range rows {
			monitor := row.toEntity()
			nextAt, err := next(monitor.Cron, now)
			if err != nil {
				// A poison cron must not block the queue forever. Disable it
				// and let the API surface the disabled flag.
				if err := tx.Exec(
					`UPDATE monitors SET enabled = FALSE, updated_at = ? WHERE id = ?`,
					now, monitor.ID,
				).Error; err != nil {
					return fmt.Errorf("disable invalid monitor: %w", err)
				}
				continue
			}
			if err := tx.Exec(
				`UPDATE monitors SET next_run_at = ?, updated_at = ? WHERE id = ?`,
				nextAt, now, monitor.ID,
			).Error; err != nil {
				return fmt.Errorf("advance monitor deadline: %w", err)
			}
			monitor.NextRunAt = nextAt
			monitor.UpdatedAt = now
			claimed = append(claimed, *monitor)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func (r *monitorRepository) RecordRun(ctx context.Context, run *entity.MonitorRun) (*entity.MonitorRun, error) {
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	var previous *entity.MonitorRun
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var prevRow monitorRunRow
		err := tx.Raw(
			`SELECT id, monitor_id, started_at, finished_at, ok, status_code, duration_ms,
			        assertion_failures, error, created_at
			   FROM monitor_runs
			  WHERE monitor_id = ?
			  ORDER BY started_at DESC, created_at DESC
			  LIMIT 1`,
			run.MonitorID,
		).Scan(&prevRow).Error
		if err != nil {
			return fmt.Errorf("read previous monitor run: %w", err)
		}
		if prevRow.ID != uuid.Nil {
			previous = prevRow.toEntity()
		}

		if err := tx.Exec(
			`INSERT INTO monitor_runs
			   (id, monitor_id, started_at, finished_at, ok, status_code, duration_ms,
			    assertion_failures, error, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			run.ID, run.MonitorID, run.StartedAt, run.FinishedAt, run.OK,
			run.StatusCode, run.DurationMS, run.AssertionFailures, run.Error, run.CreatedAt,
		).Error; err != nil {
			return fmt.Errorf("insert monitor run: %w", err)
		}

		return tx.Exec(
			`UPDATE monitors SET last_run_at = ?, updated_at = ? WHERE id = ?`,
			run.FinishedAt, run.CreatedAt, run.MonitorID,
		).Error
	})
	if err != nil {
		return nil, err
	}
	return previous, nil
}

// LoadWorkspaceSnapshot rebuilds the app workspace document from synced
// entities: collections are nested trees ordered by order_key, environments
// and globals use the shapes persisted by the desktop app.
func (r *monitorRepository) LoadWorkspaceSnapshot(ctx context.Context, workspaceID string) (*entity.WorkspaceSnapshot, error) {
	var name string
	err := r.db.WithContext(ctx).Raw(
		`SELECT name FROM workspaces WHERE id = ? AND deleted_at IS NULL`,
		workspaceID,
	).Scan(&name).Error
	if err != nil {
		return nil, fmt.Errorf("load workspace: %w", err)
	}
	if name == "" {
		return nil, gorm.ErrRecordNotFound
	}

	var rows []entityRow
	if err := r.db.WithContext(ctx).Raw(
		`SELECT id, kind, parent_id, order_key, payload
		   FROM entities
		  WHERE workspace_id = ? AND deleted_at IS NULL`,
		workspaceID,
	).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load workspace entities: %w", err)
	}

	snapshot := &entity.WorkspaceSnapshot{
		ID:           workspaceID,
		Name:         name,
		Collections:  []json.RawMessage{},
		Environments: []json.RawMessage{},
		Globals:      json.RawMessage("[]"),
	}

	children := make(map[string][]entityRow)
	roots := make([]entityRow, 0)
	for _, row := range rows {
		if row.ParentID.Valid && row.ParentID.String != "" {
			children[row.ParentID.String] = append(children[row.ParentID.String], row)
		} else {
			roots = append(roots, row)
		}
	}

	for _, row := range roots {
		switch row.Kind {
		case entity.KindCollection:
			obj, err := buildTree(row, children)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(obj)
			if err != nil {
				return nil, fmt.Errorf("encode collection %s: %w", row.ID, err)
			}
			snapshot.Collections = append(snapshot.Collections, raw)
		case entity.KindEnvironment:
			env, active, err := buildEnvironment(row)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(env)
			if err != nil {
				return nil, fmt.Errorf("encode environment %s: %w", row.ID, err)
			}
			snapshot.Environments = append(snapshot.Environments, raw)
			if active {
				id := row.ID
				snapshot.ActiveEnvID = &id
			}
		case entity.KindGlobals:
			globals, activeEnv, err := buildGlobals(row)
			if err != nil {
				return nil, err
			}
			snapshot.Globals = globals
			if activeEnv != nil {
				snapshot.ActiveEnvID = activeEnv
			}
		}
	}
	return snapshot, nil
}

func buildTree(row entityRow, children map[string][]entityRow) (map[string]any, error) {
	obj, err := decodeObject(row)
	if err != nil {
		return nil, err
	}
	if row.Kind != entity.KindCollection && row.Kind != entity.KindFolder {
		return obj, nil
	}

	kids := children[row.ID]
	sortRows(kids)
	items := make([]any, 0, len(kids))
	for _, child := range kids {
		childObj, err := buildTree(child, children)
		if err != nil {
			return nil, err
		}
		items = append(items, childObj)
	}
	if len(items) > 0 {
		obj["items"] = items
	} else if _, ok := obj["items"]; !ok {
		obj["items"] = []any{}
	}
	return obj, nil
}

func buildEnvironment(row entityRow) (map[string]any, bool, error) {
	obj, err := decodeObject(row)
	if err != nil {
		return nil, false, err
	}
	if wrapped, ok := obj["environment"].(map[string]any); ok {
		obj = wrapped
	}
	obj["id"] = row.ID
	if _, ok := obj["variables"]; !ok {
		obj["variables"] = []any{}
	}
	active, _ := obj["active"].(bool)
	delete(obj, "active")
	return obj, active, nil
}

func buildGlobals(row entityRow) (json.RawMessage, *string, error) {
	var decoded any
	if err := json.Unmarshal(row.Payload, &decoded); err != nil {
		return nil, nil, fmt.Errorf("decode globals %s: %w", row.ID, err)
	}

	var activeEnv *string
	switch value := decoded.(type) {
	case []any:
		raw, err := json.Marshal(value)
		return raw, nil, err
	case map[string]any:
		if raw, ok := value["activeEnvId"].(string); ok && raw != "" {
			id := raw
			activeEnv = &id
		}
		delete(value, "activeEnvId")
		for _, key := range []string{"variables", "globals"} {
			if vars, ok := value[key].([]any); ok {
				raw, err := json.Marshal(vars)
				return raw, activeEnv, err
			}
		}
		if _, ok := value["key"]; ok {
			return row.Payload, activeEnv, nil
		}
	}
	return json.RawMessage("[]"), activeEnv, nil
}

func decodeObject(row entityRow) (map[string]any, error) {
	var obj map[string]any
	if err := json.Unmarshal(row.Payload, &obj); err != nil {
		return nil, fmt.Errorf("decode %s %s: %w", row.Kind, row.ID, err)
	}
	if _, ok := obj["id"]; !ok {
		obj["id"] = row.ID
	}
	return obj, nil
}

func sortRows(rows []entityRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].OrderKey, rows[j].OrderKey
		switch {
		case left.Valid && right.Valid:
			if left.String != right.String {
				return left.String < right.String
			}
		case left.Valid:
			return true
		case right.Valid:
			return false
		}
		return rows[i].ID < rows[j].ID
	})
}

type monitorRow struct {
	ID           uuid.UUID      `gorm:"column:id"`
	WorkspaceID  string         `gorm:"column:workspace_id"`
	Name         string         `gorm:"column:name"`
	RequestID    string         `gorm:"column:request_id"`
	Cron         string         `gorm:"column:cron"`
	Enabled      bool           `gorm:"column:enabled"`
	AlertWebhook sql.NullString `gorm:"column:alert_webhook"`
	NextRunAt    time.Time      `gorm:"column:next_run_at"`
	LastRunAt    sql.NullTime   `gorm:"column:last_run_at"`
	CreatedBy    uuid.UUID      `gorm:"column:created_by"`
	CreatedAt    time.Time      `gorm:"column:created_at"`
	UpdatedAt    time.Time      `gorm:"column:updated_at"`
}

func (r monitorRow) toEntity() *entity.Monitor {
	m := &entity.Monitor{
		ID:          r.ID,
		WorkspaceID: r.WorkspaceID,
		Name:        r.Name,
		RequestID:   r.RequestID,
		Cron:        r.Cron,
		Enabled:     r.Enabled,
		NextRunAt:   r.NextRunAt,
		CreatedBy:   r.CreatedBy,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
	if r.AlertWebhook.Valid {
		webhook := r.AlertWebhook.String
		m.AlertWebhook = &webhook
	}
	if r.LastRunAt.Valid {
		last := r.LastRunAt.Time
		m.LastRunAt = &last
	}
	return m
}

type monitorRunRow struct {
	ID                uuid.UUID      `gorm:"column:id"`
	MonitorID         uuid.UUID      `gorm:"column:monitor_id"`
	StartedAt         time.Time      `gorm:"column:started_at"`
	FinishedAt        sql.NullTime   `gorm:"column:finished_at"`
	OK                bool           `gorm:"column:ok"`
	StatusCode        int            `gorm:"column:status_code"`
	DurationMS        int            `gorm:"column:duration_ms"`
	AssertionFailures int            `gorm:"column:assertion_failures"`
	Error             sql.NullString `gorm:"column:error"`
	CreatedAt         time.Time      `gorm:"column:created_at"`
}

func (r monitorRunRow) toEntity() *entity.MonitorRun {
	run := &entity.MonitorRun{
		ID:                r.ID,
		MonitorID:         r.MonitorID,
		StartedAt:         r.StartedAt,
		OK:                r.OK,
		StatusCode:        r.StatusCode,
		DurationMS:        r.DurationMS,
		AssertionFailures: r.AssertionFailures,
		CreatedAt:         r.CreatedAt,
	}
	if r.FinishedAt.Valid {
		finished := r.FinishedAt.Time
		run.FinishedAt = &finished
	}
	if r.Error.Valid {
		msg := r.Error.String
		run.Error = &msg
	}
	return run
}

type entityRow struct {
	ID       string         `gorm:"column:id"`
	Kind     string         `gorm:"column:kind"`
	ParentID sql.NullString `gorm:"column:parent_id"`
	OrderKey sql.NullString `gorm:"column:order_key"`
	Payload  []byte         `gorm:"column:payload"`
}

// isUniqueViolation covers both Postgres drivers in use: pgx (GORM's driver)
// exposes SQLState, lib/pq exposes Code. Keeping this driver-agnostic avoids a
// direct pgx import for one error check.
func isUniqueViolation(err error) bool {
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		return state.SQLState() == "23505"
	}
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}
