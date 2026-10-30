package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

type fakeAuth struct {
	roles map[uuid.UUID]entity.Role
}

func (f *fakeAuth) GetMember(_ context.Context, workspaceID string, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	role, ok := f.roles[userID]
	if !ok || workspaceID != "ws_1" {
		return nil, gorm.ErrRecordNotFound
	}
	return &entity.WorkspaceMember{WorkspaceID: workspaceID, UserID: userID, Role: role}, nil
}

type fakeMonitorStore struct {
	monitors    map[uuid.UUID]*entity.Monitor
	requests    map[string]bool
	runs        []entity.MonitorRun
	createErr   error
	updateErr   error
	listLimits  []int
	deleted     []uuid.UUID
	lastCreated *entity.Monitor
}

func newFakeMonitorStore() *fakeMonitorStore {
	return &fakeMonitorStore{
		monitors: map[uuid.UUID]*entity.Monitor{},
		requests: map[string]bool{"req_1": true},
	}
}

func (f *fakeMonitorStore) CreateMonitor(_ context.Context, monitor *entity.Monitor) error {
	if f.createErr != nil {
		return f.createErr
	}
	for _, existing := range f.monitors {
		if existing.WorkspaceID == monitor.WorkspaceID && existing.Name == monitor.Name {
			return domainrepo.ErrMonitorNameExists
		}
	}
	f.monitors[monitor.ID] = monitor
	f.lastCreated = monitor
	return nil
}

func (f *fakeMonitorStore) GetMonitor(_ context.Context, id uuid.UUID) (*entity.Monitor, error) {
	monitor, ok := f.monitors[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return monitor, nil
}

func (f *fakeMonitorStore) ListMonitors(_ context.Context, workspaceID string) ([]entity.Monitor, error) {
	out := []entity.Monitor{}
	for _, monitor := range f.monitors {
		if monitor.WorkspaceID == workspaceID {
			out = append(out, *monitor)
		}
	}
	return out, nil
}

func (f *fakeMonitorStore) UpdateMonitor(_ context.Context, monitor *entity.Monitor, nextRunAt *time.Time) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	if nextRunAt != nil {
		monitor.NextRunAt = *nextRunAt
	}
	f.monitors[monitor.ID] = monitor
	return nil
}

func (f *fakeMonitorStore) DeleteMonitor(_ context.Context, id uuid.UUID) error {
	if _, ok := f.monitors[id]; !ok {
		return gorm.ErrRecordNotFound
	}
	delete(f.monitors, id)
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeMonitorStore) RequestExists(_ context.Context, workspaceID, requestID string) (bool, error) {
	return workspaceID == "ws_1" && f.requests[requestID], nil
}

func (f *fakeMonitorStore) ListRuns(_ context.Context, _ uuid.UUID, limit int) ([]entity.MonitorRun, error) {
	f.listLimits = append(f.listLimits, limit)
	return f.runs, nil
}

type fakeManualExecutor struct {
	called int
	run    *entity.MonitorRun
}

func (f *fakeManualExecutor) Execute(context.Context, *entity.Monitor) (*entity.MonitorRun, error) {
	f.called++
	if f.run == nil {
		f.run = &entity.MonitorRun{ID: uuid.New(), OK: true}
	}
	return f.run, nil
}

func newMonitorFixture(role entity.Role) (*monitorUseCase, *fakeMonitorStore, *fakeManualExecutor, uuid.UUID) {
	user := uuid.New()
	store := newFakeMonitorStore()
	executor := &fakeManualExecutor{}
	uc := &monitorUseCase{
		workspaces: &fakeAuth{roles: map[uuid.UUID]entity.Role{user: role}},
		monitors:   store,
		executor:   executor,
	}
	return uc, store, executor, user
}

func TestCreateMonitorValidatesAndComputesNextRun(t *testing.T) {
	uc, store, _, editor := newMonitorFixture(entity.RoleEditor)
	before := time.Now()

	monitor, err := uc.Create(context.Background(), editor, "ws_1", CreateMonitorInput{
		Name:      "  Health check ",
		RequestID: "req_1",
		Cron:      "*/5 * * * *",
	})
	require.NoError(t, err)
	require.Equal(t, "Health check", monitor.Name)
	require.True(t, monitor.Enabled)
	require.NotEqual(t, uuid.Nil, monitor.ID)
	require.Equal(t, editor, monitor.CreatedBy)
	require.True(t, monitor.NextRunAt.After(before))
	require.WithinDuration(t, before.UTC().Truncate(time.Minute).Add(5*time.Minute), monitor.NextRunAt, 6*time.Minute)
	require.Contains(t, store.monitors, monitor.ID)

	// Explicitly disabled monitors still get a deadline; the worker filters on
	// enabled.
	disabled := false
	other, err := uc.Create(context.Background(), editor, "ws_1", CreateMonitorInput{
		Name: "Disabled", RequestID: "req_1", Cron: "@hourly", Enabled: &disabled,
	})
	require.NoError(t, err)
	require.False(t, other.Enabled)
	require.False(t, other.NextRunAt.IsZero())
}

func TestCreateMonitorValidationAndRoles(t *testing.T) {
	uc, _, _, editor := newMonitorFixture(entity.RoleEditor)
	viewer := uuid.New()
	uc.workspaces = &fakeAuth{roles: map[uuid.UUID]entity.Role{
		editor: entity.RoleEditor,
		viewer: entity.RoleViewer,
	}}
	outsider := uuid.New()

	cases := []struct {
		name    string
		input   CreateMonitorInput
		wantErr error
	}{
		{"missing name", CreateMonitorInput{RequestID: "req_1", Cron: "* * * * *"}, ErrValidation},
		{"missing request", CreateMonitorInput{Name: "n", Cron: "* * * * *"}, ErrValidation},
		{"unknown request", CreateMonitorInput{Name: "n", RequestID: "req_x", Cron: "* * * * *"}, ErrValidation},
		{"bad cron", CreateMonitorInput{Name: "n", RequestID: "req_1", Cron: "nope"}, ErrValidation},
		{"bad webhook", CreateMonitorInput{Name: "n", RequestID: "req_1", Cron: "* * * * *", AlertWebhook: stringPtr("ftp://x")}, ErrValidation},
		{"name too long", CreateMonitorInput{Name: string(make([]byte, 121)), RequestID: "req_1", Cron: "* * * * *"}, ErrValidation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := uc.Create(context.Background(), editor, "ws_1", tc.input)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}

	_, err := uc.Create(context.Background(), viewer, "ws_1", CreateMonitorInput{Name: "n", RequestID: "req_1", Cron: "* * * * *"})
	require.ErrorIs(t, err, ErrForbidden)

	_, err = uc.Create(context.Background(), outsider, "ws_1", CreateMonitorInput{Name: "n", RequestID: "req_1", Cron: "* * * * *"})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestCreateMonitorDuplicateNameConflicts(t *testing.T) {
	uc, store, _, editor := newMonitorFixture(entity.RoleEditor)
	store.createErr = domainrepo.ErrMonitorNameExists
	_, err := uc.Create(context.Background(), editor, "ws_1", CreateMonitorInput{
		Name: "dup", RequestID: "req_1", Cron: "* * * * *",
	})
	require.ErrorIs(t, err, ErrConflict)
}

func TestUpdateMonitorRecalculatesAndClearsWebhook(t *testing.T) {
	uc, store, _, editor := newMonitorFixture(entity.RoleEditor)
	monitor, err := uc.Create(context.Background(), editor, "ws_1", CreateMonitorInput{
		Name: "n", RequestID: "req_1", Cron: "0 9 * * *",
	})
	require.NoError(t, err)
	webhook := "https://hooks.example.test/relay"
	updated, err := uc.Update(context.Background(), editor, monitor.ID, UpdateMonitorInput{
		AlertWebhook: &webhook, AlertWebhookSet: true,
	})
	require.NoError(t, err)
	require.NotNil(t, updated.AlertWebhook)
	require.Equal(t, webhook, *updated.AlertWebhook)

	oldNext := updated.NextRunAt
	updated, err = uc.Update(context.Background(), editor, monitor.ID, UpdateMonitorInput{
		Cron: stringPtr("*/15 * * * *"), Enabled: boolPtr(true),
	})
	require.NoError(t, err)
	require.Equal(t, "*/15 * * * *", updated.Cron)
	require.False(t, updated.NextRunAt.Equal(oldNext))

	updated, err = uc.Update(context.Background(), editor, monitor.ID, UpdateMonitorInput{AlertWebhookSet: true})
	require.NoError(t, err)
	require.Nil(t, updated.AlertWebhook)

	// The store receives the mutated monitor.
	require.Equal(t, "*/15 * * * *", store.monitors[monitor.ID].Cron)
}

func TestUpdateMonitorRejectsInvalidRequestAndRole(t *testing.T) {
	uc, _, _, editor := newMonitorFixture(entity.RoleEditor)
	monitor, err := uc.Create(context.Background(), editor, "ws_1", CreateMonitorInput{
		Name: "n", RequestID: "req_1", Cron: "* * * * *",
	})
	require.NoError(t, err)

	_, err = uc.Update(context.Background(), editor, monitor.ID, UpdateMonitorInput{RequestID: stringPtr("req_missing")})
	require.ErrorIs(t, err, ErrValidation)

	viewer := uuid.New()
	uc.workspaces = &fakeAuth{roles: map[uuid.UUID]entity.Role{
		editor: entity.RoleEditor,
		viewer: entity.RoleViewer,
	}}
	_, err = uc.Update(context.Background(), viewer, monitor.ID, UpdateMonitorInput{Name: stringPtr("new")})
	require.ErrorIs(t, err, ErrForbidden)
}

func TestListAndGetRulesAndRunLimit(t *testing.T) {
	uc, store, exec, editor := newMonitorFixture(entity.RoleEditor)
	monitor, err := uc.Create(context.Background(), editor, "ws_1", CreateMonitorInput{
		Name: "n", RequestID: "req_1", Cron: "* * * * *",
	})
	require.NoError(t, err)

	list, err := uc.List(context.Background(), editor, "ws_1")
	require.NoError(t, err)
	require.Len(t, list, 1)

	got, err := uc.Get(context.Background(), editor, monitor.ID)
	require.NoError(t, err)
	require.Equal(t, monitor.ID, got.ID)

	_, err = uc.ListRuns(context.Background(), editor, monitor.ID, 5000)
	require.NoError(t, err)
	require.Equal(t, 500, store.listLimits[len(store.listLimits)-1])

	_, err = uc.ListRuns(context.Background(), editor, monitor.ID, 0)
	require.NoError(t, err)
	require.Equal(t, 50, store.listLimits[len(store.listLimits)-1])

	viewer := uuid.New()
	uc.workspaces = &fakeAuth{roles: map[uuid.UUID]entity.Role{
		editor: entity.RoleEditor,
		viewer: entity.RoleViewer,
	}}
	_, err = uc.ListRuns(context.Background(), viewer, monitor.ID, 10)
	require.NoError(t, err)
	_, err = uc.RunNow(context.Background(), viewer, monitor.ID)
	require.ErrorIs(t, err, ErrForbidden)
	require.Zero(t, exec.called)

	run, err := uc.RunNow(context.Background(), editor, monitor.ID)
	require.NoError(t, err)
	require.True(t, run.OK)
	require.Equal(t, 1, exec.called)

	require.NoError(t, uc.Delete(context.Background(), editor, monitor.ID))
	_, err = uc.Get(context.Background(), editor, monitor.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMonitorUseCaseMissingDependencies(t *testing.T) {
	// A deleted monitor is indistinguishable from an unknown id.
	uc, _, _, editor := newMonitorFixture(entity.RoleEditor)
	_, err := uc.Get(context.Background(), editor, uuid.New())
	require.True(t, errors.Is(err, ErrNotFound))

	// A nil executor cannot pretend a manual run succeeded.
	uc.executor = nil
	monitor, err := uc.Create(context.Background(), editor, "ws_1", CreateMonitorInput{
		Name: "n", RequestID: "req_1", Cron: "* * * * *",
	})
	require.NoError(t, err)
	_, err = uc.RunNow(context.Background(), editor, monitor.ID)
	require.Error(t, err)
}

func stringPtr(value string) *string { return &value }

func boolPtr(value bool) *bool { return &value }
