package usecase

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
	"github.com/abdullahPrasetio/relay-api/internal/monitor"
)

// MonitorExecutor runs a monitor once. The worker and the manual-run endpoint
// share the same implementation.
type MonitorExecutor interface {
	Execute(ctx context.Context, monitor *entity.Monitor) (*entity.MonitorRun, error)
}

// CreateMonitorInput is the validated create payload.
type CreateMonitorInput struct {
	Name         string
	RequestID    string
	Cron         string
	Enabled      *bool
	AlertWebhook *string
}

// UpdateMonitorInput uses pointers to distinguish "absent" from "set to
// false/empty". AlertWebhookSet allows PATCH {"alertWebhook": null} to clear.
type UpdateMonitorInput struct {
	Name            *string
	RequestID       *string
	Cron            *string
	Enabled         *bool
	AlertWebhook    *string
	AlertWebhookSet bool
}

// MonitorUseCase is the scheduled-monitor service. Reads use the pull
// permission (viewer+), mutations use push (editor+), matching the workspace
// role matrix.
type MonitorUseCase interface {
	List(ctx context.Context, userID uuid.UUID, workspaceID string) ([]entity.Monitor, error)
	Get(ctx context.Context, userID uuid.UUID, monitorID uuid.UUID) (*entity.Monitor, error)
	Create(ctx context.Context, userID uuid.UUID, workspaceID string, input CreateMonitorInput) (*entity.Monitor, error)
	Update(ctx context.Context, userID uuid.UUID, monitorID uuid.UUID, input UpdateMonitorInput) (*entity.Monitor, error)
	Delete(ctx context.Context, userID uuid.UUID, monitorID uuid.UUID) error
	ListRuns(ctx context.Context, userID uuid.UUID, monitorID uuid.UUID, limit int) ([]entity.MonitorRun, error)
	RunNow(ctx context.Context, userID uuid.UUID, monitorID uuid.UUID) (*entity.MonitorRun, error)
}

type monitorUseCase struct {
	workspaces WorkspaceAuthorizer
	monitors   MonitorStore
	executor   MonitorExecutor
}

// WorkspaceAuthorizer resolves the caller's workspace role.
type WorkspaceAuthorizer interface {
	GetMember(ctx context.Context, workspaceID string, userID uuid.UUID) (*entity.WorkspaceMember, error)
}

// MonitorStore is the persistence surface used by the monitor usecase.
type MonitorStore interface {
	GetMonitor(ctx context.Context, id uuid.UUID) (*entity.Monitor, error)
	ListMonitors(ctx context.Context, workspaceID string) ([]entity.Monitor, error)
	CreateMonitor(ctx context.Context, monitor *entity.Monitor) error
	UpdateMonitor(ctx context.Context, monitor *entity.Monitor, nextRunAt *time.Time) error
	DeleteMonitor(ctx context.Context, id uuid.UUID) error
	RequestExists(ctx context.Context, workspaceID, requestID string) (bool, error)
	ListRuns(ctx context.Context, monitorID uuid.UUID, limit int) ([]entity.MonitorRun, error)
}

// NewMonitorUseCase wires the monitor repository, workspace authorization and
// executor into the usecase.
func NewMonitorUseCase(
	workspaces WorkspaceAuthorizer,
	monitors MonitorStore,
	executor MonitorExecutor,
) MonitorUseCase {
	return &monitorUseCase{workspaces: workspaces, monitors: monitors, executor: executor}
}

func (u *monitorUseCase) List(ctx context.Context, userID uuid.UUID, workspaceID string) ([]entity.Monitor, error) {
	if err := u.authorize(ctx, userID, workspaceID, entity.PermissionPull); err != nil {
		return nil, err
	}
	return u.monitors.ListMonitors(ctx, workspaceID)
}

func (u *monitorUseCase) Get(ctx context.Context, userID uuid.UUID, monitorID uuid.UUID) (*entity.Monitor, error) {
	found, err := u.monitors.GetMonitor(ctx, monitorID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := u.authorize(ctx, userID, found.WorkspaceID, entity.PermissionPull); err != nil {
		return nil, err
	}
	return found, nil
}

func (u *monitorUseCase) Create(ctx context.Context, userID uuid.UUID, workspaceID string, input CreateMonitorInput) (*entity.Monitor, error) {
	if err := u.authorize(ctx, userID, workspaceID, entity.PermissionPush); err != nil {
		return nil, err
	}

	name, err := validMonitorName(input.Name)
	if err != nil {
		return nil, err
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return nil, fmt.Errorf("%w: requestId is required", ErrValidation)
	}
	if err := u.requireRequest(ctx, workspaceID, requestID); err != nil {
		return nil, err
	}
	cronSpec, err := validCron(input.Cron)
	if err != nil {
		return nil, err
	}
	nextRunAt, err := monitor.NextRun(cronSpec, time.Now())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	webhook, err := validWebhook(input.AlertWebhook)
	if err != nil {
		return nil, err
	}

	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}

	entry := &entity.Monitor{
		ID:           uuid.New(),
		WorkspaceID:  workspaceID,
		Name:         name,
		RequestID:    requestID,
		Cron:         cronSpec,
		Enabled:      enabled,
		AlertWebhook: webhook,
		NextRunAt:    nextRunAt,
		CreatedBy:    userID,
	}
	if err := u.monitors.CreateMonitor(ctx, entry); err != nil {
		return nil, mapMonitorError(err)
	}
	return entry, nil
}

func (u *monitorUseCase) Update(ctx context.Context, userID uuid.UUID, monitorID uuid.UUID, input UpdateMonitorInput) (*entity.Monitor, error) {
	entry, err := u.monitors.GetMonitor(ctx, monitorID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := u.authorize(ctx, userID, entry.WorkspaceID, entity.PermissionPush); err != nil {
		return nil, err
	}

	if input.Name != nil {
		name, err := validMonitorName(*input.Name)
		if err != nil {
			return nil, err
		}
		entry.Name = name
	}
	if input.RequestID != nil {
		requestID := strings.TrimSpace(*input.RequestID)
		if requestID == "" {
			return nil, fmt.Errorf("%w: requestId is required", ErrValidation)
		}
		if err := u.requireRequest(ctx, entry.WorkspaceID, requestID); err != nil {
			return nil, err
		}
		entry.RequestID = requestID
	}

	recompute := false
	var nextRunAt *time.Time
	if input.Cron != nil {
		cronSpec, err := validCron(*input.Cron)
		if err != nil {
			return nil, err
		}
		if cronSpec != entry.Cron {
			entry.Cron = cronSpec
			recompute = true
		}
	}
	if input.Enabled != nil {
		if *input.Enabled && !entry.Enabled {
			recompute = true
		}
		entry.Enabled = *input.Enabled
	}
	if recompute {
		next, err := monitor.NextRun(entry.Cron, time.Now())
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrValidation, err)
		}
		nextRunAt = &next
		entry.NextRunAt = next
	}
	if input.AlertWebhookSet {
		webhook, err := validWebhook(input.AlertWebhook)
		if err != nil {
			return nil, err
		}
		entry.AlertWebhook = webhook
	}

	if err := u.monitors.UpdateMonitor(ctx, entry, nextRunAt); err != nil {
		return nil, mapMonitorError(err)
	}
	return entry, nil
}

func (u *monitorUseCase) Delete(ctx context.Context, userID uuid.UUID, monitorID uuid.UUID) error {
	entry, err := u.monitors.GetMonitor(ctx, monitorID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := u.authorize(ctx, userID, entry.WorkspaceID, entity.PermissionPush); err != nil {
		return err
	}
	if err := u.monitors.DeleteMonitor(ctx, monitorID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (u *monitorUseCase) ListRuns(ctx context.Context, userID uuid.UUID, monitorID uuid.UUID, limit int) ([]entity.MonitorRun, error) {
	entry, err := u.monitors.GetMonitor(ctx, monitorID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := u.authorize(ctx, userID, entry.WorkspaceID, entity.PermissionPull); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	return u.monitors.ListRuns(ctx, monitorID, limit)
}

func (u *monitorUseCase) RunNow(ctx context.Context, userID uuid.UUID, monitorID uuid.UUID) (*entity.MonitorRun, error) {
	entry, err := u.monitors.GetMonitor(ctx, monitorID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := u.authorize(ctx, userID, entry.WorkspaceID, entity.PermissionPush); err != nil {
		return nil, err
	}
	if u.executor == nil {
		return nil, errors.New("monitor executor is not configured")
	}
	return u.executor.Execute(ctx, entry)
}

func (u *monitorUseCase) requireRequest(ctx context.Context, workspaceID, requestID string) error {
	exists, err := u.monitors.RequestExists(ctx, workspaceID, requestID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: request %q does not exist in this workspace", ErrValidation, requestID)
	}
	return nil
}

func (u *monitorUseCase) authorize(ctx context.Context, userID uuid.UUID, workspaceID string, permission entity.Permission) error {
	if strings.TrimSpace(workspaceID) == "" {
		return fmt.Errorf("%w: workspace id is required", ErrValidation)
	}
	if userID == uuid.Nil {
		return fmt.Errorf("%w: user id is required", ErrValidation)
	}
	member, err := u.workspaces.GetMember(ctx, workspaceID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if !member.Role.Allows(permission) {
		return fmt.Errorf("%w: role %q cannot %s", ErrForbidden, member.Role, permission)
	}
	return nil
}

func validMonitorName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: name is required", ErrValidation)
	}
	if len([]rune(name)) > 120 {
		return "", fmt.Errorf("%w: name must be at most 120 characters", ErrValidation)
	}
	return name, nil
}

func validCron(spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", fmt.Errorf("%w: cron is required", ErrValidation)
	}
	if _, err := monitor.ParseCron(spec); err != nil {
		return "", fmt.Errorf("%w: %v", ErrValidation, err)
	}
	return spec, nil
}

func validWebhook(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	target := strings.TrimSpace(*raw)
	if target == "" {
		return nil, nil
	}
	if len(target) > 2048 {
		return nil, fmt.Errorf("%w: alertWebhook is too long", ErrValidation)
	}
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("%w: alertWebhook must be an http(s) URL", ErrValidation)
	}
	return &target, nil
}

func mapMonitorError(err error) error {
	if errors.Is(err, domainrepo.ErrMonitorNameExists) {
		return fmt.Errorf("%w: monitor name already exists", ErrConflict)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
