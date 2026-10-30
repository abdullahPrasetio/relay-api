//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	"github.com/abdullahPrasetio/relay-api/internal/repository/db"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/migrations"
)

// Requires a reachable PostgreSQL database, e.g.:
//
//	TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55432/phase2_invite?sslmode=disable' \
//	  go test -tags=integration ./internal/integration/...
func TestInviteAcceptFlow(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()

	ws, err := h.workspaces.CreateWorkspace(ctx, h.owner, "Invite Team")
	require.NoError(t, err)
	_, err = h.workspaces.AddMember(ctx, h.owner, ws.ID, h.admin, entity.RoleAdmin)
	require.NoError(t, err)
	_, err = h.workspaces.AddMember(ctx, h.owner, ws.ID, h.editor, entity.RoleEditor)
	require.NoError(t, err)

	requireAuditActions(t, h, ws.ID, "workspace.create")

	// Only admin+ may invite.
	_, _, err = h.invites.CreateInvite(ctx, h.editor, ws.ID, entity.RoleViewer, time.Hour)
	require.ErrorIs(t, err, usecase.ErrForbidden)
	_, _, err = h.invites.CreateInvite(ctx, h.outsider, ws.ID, entity.RoleViewer, time.Hour)
	require.ErrorIs(t, err, usecase.ErrNotFound)
	_, _, err = h.invites.CreateInvite(ctx, h.owner, ws.ID, entity.RoleOwner, time.Hour)
	require.ErrorIs(t, err, usecase.ErrValidation)
	_, _, err = h.invites.CreateInvite(ctx, h.admin, ws.ID, entity.RoleAdmin, time.Hour)
	require.ErrorIs(t, err, usecase.ErrForbidden)

	// Admin may invite editors.
	token, expiresAt, err := h.invites.CreateInvite(ctx, h.admin, ws.ID, entity.RoleEditor, 2*time.Hour)
	require.NoError(t, err)
	require.Len(t, token, 64)
	require.WithinDuration(t, time.Now().Add(2*time.Hour), expiresAt, time.Minute)

	// Only the hash is persisted.
	var storedHash string
	require.NoError(t, h.gdb.Raw(`SELECT token_hash FROM invites WHERE workspace_id = ?`, ws.ID).Row().Scan(&storedHash))
	sum := sha256.Sum256([]byte(token))
	require.Equal(t, hex.EncodeToString(sum[:]), storedHash)
	require.NotEqual(t, token, storedHash)

	// Public metadata exposes the workspace name, role and expiry.
	info, err := h.invites.GetInvite(ctx, token)
	require.NoError(t, err)
	require.Equal(t, ws.ID, info.WorkspaceID)
	require.Equal(t, "Invite Team", info.WorkspaceName)
	require.Equal(t, entity.RoleEditor, info.Role)
	require.WithinDuration(t, expiresAt, info.ExpiresAt, time.Second)

	// Accept grants membership with the invited role.
	joined, role, err := h.invites.AcceptInvite(ctx, h.outsider, token)
	require.NoError(t, err)
	require.Equal(t, ws.ID, joined.ID)
	require.Equal(t, "Invite Team", joined.Name)
	require.Equal(t, entity.RoleEditor, role)

	members, err := h.workspaces.ListMembers(ctx, h.owner, ws.ID)
	require.NoError(t, err)
	require.Len(t, members, 4)
	require.Equal(t, entity.RoleEditor, memberRole(t, members, h.outsider))

	// The accepted user can now push.
	_, _, err = h.workspaces.PushChanges(ctx, ws.ID, h.outsider, []entity.Change{{
		OpID: "op-invited", ID: "req_invited", Kind: entity.KindRequest,
		Payload: []byte(`{"name":"from invite"}`),
	}})
	require.NoError(t, err)

	// Single use: a second accept is rejected, even for a different user.
	_, _, err = h.invites.AcceptInvite(ctx, h.viewer, token)
	require.ErrorIs(t, err, usecase.ErrInviteUsed)
	_, _, err = h.invites.AcceptInvite(ctx, h.outsider, token)
	require.ErrorIs(t, err, usecase.ErrInviteUsed)
	_, err = h.invites.GetInvite(ctx, token)
	require.ErrorIs(t, err, usecase.ErrInviteUsed)

	members, err = h.workspaces.ListMembers(ctx, h.owner, ws.ID)
	require.NoError(t, err)
	require.Len(t, members, 4, "rejected accepts must not add members")

	// Invite create + accept are audited; accepted actor is recorded.
	events := requireAuditActions(t, h, ws.ID, "invite.create", "invite.accept")
	var accept entity.AuditEvent
	for _, event := range events {
		if event.Action == "invite.accept" {
			accept = event
		}
	}
	require.Equal(t, h.outsider, accept.ActorID)
}

func TestInviteExpiryAndAlreadyMember(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()

	ws, err := h.workspaces.CreateWorkspace(ctx, h.owner, "Expiry Team")
	require.NoError(t, err)
	_, err = h.workspaces.AddMember(ctx, h.owner, ws.ID, h.viewer, entity.RoleViewer)
	require.NoError(t, err)

	expiredToken, _, err := h.invites.CreateInvite(ctx, h.owner, ws.ID, entity.RoleEditor, time.Millisecond)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	_, err = h.invites.GetInvite(ctx, expiredToken)
	require.ErrorIs(t, err, usecase.ErrInviteExpired)
	_, _, err = h.invites.AcceptInvite(ctx, h.outsider, expiredToken)
	require.ErrorIs(t, err, usecase.ErrInviteExpired)

	// An existing member is rejected and the invite is not consumed.
	memberToken, _, err := h.invites.CreateInvite(ctx, h.owner, ws.ID, entity.RoleEditor, time.Hour)
	require.NoError(t, err)
	_, _, err = h.invites.AcceptInvite(ctx, h.viewer, memberToken)
	require.ErrorIs(t, err, usecase.ErrAlreadyMember)

	_, role, err := h.invites.AcceptInvite(ctx, h.outsider, memberToken)
	require.NoError(t, err)
	require.Equal(t, entity.RoleEditor, role)

	// Unknown tokens are indistinguishable from missing invites.
	_, _, err = h.invites.AcceptInvite(ctx, h.admin, "deadbeef")
	require.ErrorIs(t, err, usecase.ErrNotFound)
	_, err = h.invites.GetInvite(ctx, "deadbeef")
	require.ErrorIs(t, err, usecase.ErrNotFound)
}

func TestAuditLogCoverageAndAccess(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()

	ws, err := h.workspaces.CreateWorkspace(ctx, h.owner, "Audit Team")
	require.NoError(t, err)

	_, err = h.workspaces.AddMember(ctx, h.owner, ws.ID, h.admin, entity.RoleAdmin)
	require.NoError(t, err)
	_, err = h.workspaces.AddMember(ctx, h.owner, ws.ID, h.editor, entity.RoleEditor)
	require.NoError(t, err)
	_, err = h.workspaces.AddMember(ctx, h.owner, ws.ID, h.viewer, entity.RoleViewer)
	require.NoError(t, err)

	_, err = h.workspaces.UpdateMemberRole(ctx, h.owner, ws.ID, h.viewer, entity.RoleEditor)
	require.NoError(t, err)
	require.NoError(t, h.workspaces.RemoveMember(ctx, h.owner, ws.ID, h.viewer))

	_, err = h.workspaces.RenameWorkspace(ctx, h.owner, ws.ID, "Audit Team Renamed")
	require.NoError(t, err)

	_, _, err = h.invites.CreateInvite(ctx, h.owner, ws.ID, entity.RoleViewer, time.Hour)
	require.NoError(t, err)

	// Admin+ only. Members without admin see 403, non-members 404.
	_, _, err = h.workspaces.ListAudit(ctx, h.editor, ws.ID, 0, 50)
	require.ErrorIs(t, err, usecase.ErrForbidden)
	_, _, err = h.workspaces.ListAudit(ctx, h.outsider, ws.ID, 0, 50)
	require.ErrorIs(t, err, usecase.ErrNotFound)

	events := requireAuditActions(t, h, ws.ID,
		"workspace.create", "workspace.rename",
		"member.add", "member.role_change", "member.remove",
		"invite.create",
	)
	for i := 1; i < len(events); i++ {
		require.Greater(t, events[i-1].ID, events[i].ID, "audit must be newest-first")
	}

	// Cursor pagination walks older events without duplicates.
	first, nextBefore, err := h.workspaces.ListAudit(ctx, h.admin, ws.ID, 0, 3)
	require.NoError(t, err)
	require.Len(t, first, 3)
	require.NotZero(t, nextBefore)
	second, _, err := h.workspaces.ListAudit(ctx, h.admin, ws.ID, nextBefore, 3)
	require.NoError(t, err)
	require.NotEmpty(t, second)
	for _, older := range second {
		require.Less(t, older.ID, nextBefore)
		for _, newer := range first {
			require.NotEqual(t, newer.ID, older.ID)
		}
	}

	// Deleting the workspace removes access through the usecase but keeps the
	// append-only audit rows.
	require.NoError(t, h.workspaces.DeleteWorkspace(ctx, h.owner, ws.ID))
	var deleteEvents int64
	require.NoError(t, h.gdb.Raw(
		`SELECT COUNT(*) FROM audit_events WHERE workspace_id = ? AND action = 'workspace.delete'`, ws.ID,
	).Row().Scan(&deleteEvents))
	require.EqualValues(t, 1, deleteEvents)

	_, _, err = h.workspaces.ListAudit(ctx, h.owner, ws.ID, 0, 50)
	require.ErrorIs(t, err, usecase.ErrNotFound)
}

// requireAuditActions returns all audit events for the workspace and asserts
// every wanted action is present.
func requireAuditActions(t *testing.T, h *inviteHarness, workspaceID string, actions ...string) []entity.AuditEvent {
	t.Helper()
	events, _, err := h.workspaces.ListAudit(context.Background(), h.owner, workspaceID, 0, 200)
	require.NoError(t, err)

	seen := make(map[string]bool, len(events))
	for _, event := range events {
		seen[event.Action] = true
	}
	for _, action := range actions {
		require.True(t, seen[action], "missing audit action %q (have %v)", action, seen)
	}
	for _, event := range events {
		require.Equal(t, workspaceID, event.WorkspaceID)
		require.NotEqual(t, uuid.Nil, event.ActorID)
	}
	return events
}

// memberRole returns the role of userID in members, failing the test when the
// user is absent.
func memberRole(t *testing.T, members []*entity.WorkspaceMember, userID uuid.UUID) entity.Role {
	t.Helper()
	for _, member := range members {
		if member.UserID == userID {
			return member.Role
		}
	}
	t.Fatalf("user %s is not a member", userID)
	return ""
}

type inviteHarness struct {
	workspaces usecase.WorkspaceUseCase
	invites    usecase.InviteUseCase
	gdb        *gorm.DB

	owner    uuid.UUID
	admin    uuid.UUID
	editor   uuid.UUID
	viewer   uuid.UUID
	outsider uuid.UUID
}

func newInviteHarness(t *testing.T) *inviteHarness {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	sqlDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping())

	// Fresh schema so migrations 000001–000005 are always applied from
	// scratch.
	_, err = sqlDB.Exec(`DROP SCHEMA IF EXISTS public CASCADE`)
	require.NoError(t, err)
	_, err = sqlDB.Exec(`CREATE SCHEMA public`)
	require.NoError(t, err)

	src, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	driver, err := migratepostgres.WithInstance(sqlDB, &migratepostgres.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	require.NoError(t, err)
	require.NoError(t, m.Up())

	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	t.Cleanup(func() {
		if raw, err := gdb.DB(); err == nil {
			_ = raw.Close()
		}
	})

	workspaceRepo := db.NewWorkspaceRepository(gdb)
	h := &inviteHarness{
		workspaces: usecase.NewWorkspaceUseCase(workspaceRepo),
		invites:    usecase.NewInviteUseCase(db.NewInviteRepository(gdb), workspaceRepo),
		gdb:        gdb,
		owner:      uuid.MustParse("a1111111-1111-4111-8111-111111111111"),
		admin:      uuid.MustParse("a2222222-2222-4222-8222-222222222222"),
		editor:     uuid.MustParse("a3333333-3333-4333-8333-333333333333"),
		viewer:     uuid.MustParse("a4444444-4444-4444-8444-444444444444"),
		outsider:   uuid.MustParse("a5555555-5555-4555-8555-555555555555"),
	}

	for i, id := range []uuid.UUID{h.owner, h.admin, h.editor, h.viewer, h.outsider} {
		res := gdb.Exec(
			`INSERT INTO users (id, name, email, password) VALUES (?, ?, ?, ?)`,
			id, "user", "invite-user"+string(rune('a'+i))+"@example.test", "hash",
		)
		require.NoError(t, res.Error)
	}

	return h
}
