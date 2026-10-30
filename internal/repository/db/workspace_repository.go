package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

// workspaceRepository stores workspaces, members and entity deltas. Sync
// queries are raw SQL on purpose: they must also perform the membership check
// in the same statement/transaction (plan D8).
type workspaceRepository struct {
	db *gorm.DB
}

// NewWorkspaceRepository returns the Postgres-backed workspace repository.
func NewWorkspaceRepository(db *gorm.DB) domainrepo.WorkspaceRepository {
	return &workspaceRepository{db: db}
}

func (r *workspaceRepository) CreateWorkspace(ctx context.Context, workspace *entity.Workspace, ownerRole entity.Role) error {
	now := time.Now().UTC()
	workspace.CreatedAt = now

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`INSERT INTO workspaces (id, name, owner_id, created_at) VALUES (?, ?, ?, ?)`,
			workspace.ID, workspace.Name, workspace.OwnerID, now,
		).Error; err != nil {
			return fmt.Errorf("insert workspace: %w", err)
		}
		if err := tx.Exec(
			`INSERT INTO workspace_members (workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
			workspace.ID, workspace.OwnerID, string(ownerRole), now,
		).Error; err != nil {
			return fmt.Errorf("insert workspace owner: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (r *workspaceRepository) GetWorkspace(ctx context.Context, id string) (*entity.Workspace, error) {
	var row workspaceRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, name, owner_id, created_at, deleted_at FROM workspaces WHERE id = ?`,
		id,
	).Scan(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get workspace: %w", err)
	}
	if row.ID == "" {
		return nil, gorm.ErrRecordNotFound
	}
	return row.toEntity(), nil
}

func (r *workspaceRepository) ListWorkspacesForUser(ctx context.Context, userID uuid.UUID) ([]entity.WorkspaceWithRole, error) {
	var rows []workspaceRoleRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT w.id, w.name, w.owner_id, w.created_at, w.deleted_at, m.role
		   FROM workspace_members m
		   JOIN workspaces w ON w.id = m.workspace_id
		  WHERE m.user_id = ? AND w.deleted_at IS NULL
		  ORDER BY w.created_at ASC, w.id ASC`,
		userID,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}

	out := make([]entity.WorkspaceWithRole, 0, len(rows))
	for _, row := range rows {
		out = append(out, entity.WorkspaceWithRole{
			Workspace: *row.workspaceRow().toEntity(),
			Role:      entity.Role(row.Role),
		})
	}
	return out, nil
}

func (r *workspaceRepository) RenameWorkspace(ctx context.Context, id, name string) error {
	res := r.db.WithContext(ctx).Exec(
		`UPDATE workspaces SET name = ? WHERE id = ? AND deleted_at IS NULL`, name, id,
	)
	if res.Error != nil {
		return fmt.Errorf("rename workspace: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *workspaceRepository) SoftDeleteWorkspace(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Exec(
		`UPDATE workspaces SET deleted_at = now() WHERE id = ? AND deleted_at IS NULL`, id,
	)
	if res.Error != nil {
		return fmt.Errorf("delete workspace: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *workspaceRepository) GetMember(ctx context.Context, workspaceID string, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	var row memberRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT m.workspace_id, m.user_id, m.role, m.invited_by, m.created_at
		   FROM workspace_members m
		   JOIN workspaces w ON w.id = m.workspace_id
		  WHERE m.workspace_id = ? AND m.user_id = ? AND w.deleted_at IS NULL`,
		workspaceID, userID,
	).Scan(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get workspace member: %w", err)
	}
	if row.UserID == uuid.Nil {
		return nil, gorm.ErrRecordNotFound
	}
	return row.toEntity(), nil
}

func (r *workspaceRepository) ListMembers(ctx context.Context, workspaceID string) ([]*entity.WorkspaceMember, error) {
	var rows []memberRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT workspace_id, user_id, role, invited_by, created_at
		   FROM workspace_members
		  WHERE workspace_id = ?
		  ORDER BY created_at ASC, user_id ASC`,
		workspaceID,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list workspace members: %w", err)
	}

	out := make([]*entity.WorkspaceMember, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toEntity())
	}
	return out, nil
}

func (r *workspaceRepository) AddMember(ctx context.Context, member *entity.WorkspaceMember) error {
	member.CreatedAt = time.Now().UTC()
	res := r.db.WithContext(ctx).Exec(
		`INSERT INTO workspace_members (workspace_id, user_id, role, invited_by, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (workspace_id, user_id) DO NOTHING`,
		member.WorkspaceID, member.UserID, string(member.Role), member.InvitedBy, member.CreatedAt,
	)
	if res.Error != nil {
		return fmt.Errorf("add workspace member: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return domainrepo.ErrMemberExists
	}
	return nil
}

func (r *workspaceRepository) UpdateMemberRole(ctx context.Context, workspaceID string, userID uuid.UUID, role entity.Role) error {
	res := r.db.WithContext(ctx).Exec(
		`UPDATE workspace_members SET role = ? WHERE workspace_id = ? AND user_id = ?`,
		string(role), workspaceID, userID,
	)
	if res.Error != nil {
		return fmt.Errorf("update member role: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *workspaceRepository) RemoveMember(ctx context.Context, workspaceID string, userID uuid.UUID) error {
	res := r.db.WithContext(ctx).Exec(
		`DELETE FROM workspace_members WHERE workspace_id = ? AND user_id = ?`,
		workspaceID, userID,
	)
	if res.Error != nil {
		return fmt.Errorf("remove member: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// PushChanges applies a batch in one transaction. Every change is either
// applied, returned from the entity_ops idempotency log, or reported as a
// conflict with the current server state.
func (r *workspaceRepository) PushChanges(ctx context.Context, workspaceID string, actorID uuid.UUID, changes []entity.Change) ([]entity.PushResult, int64, error) {
	results := make([]entity.PushResult, 0, len(changes))
	var serverSeq int64

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialise pushes per workspace so base_rev checks and inserts cannot
		// interleave between concurrent clients.
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, workspaceID).Error; err != nil {
			return fmt.Errorf("lock workspace: %w", err)
		}

		role, err := workspaceRole(tx, workspaceID, actorID)
		if err != nil {
			return err
		}
		if !role.Allows(entity.PermissionPush) {
			return domainrepo.ErrWriteRequired
		}

		if err := tx.Raw(
			`SELECT COALESCE(MAX(seq), 0) FROM entity_changes WHERE workspace_id = ?`, workspaceID,
		).Row().Scan(&serverSeq); err != nil {
			return fmt.Errorf("read workspace seq: %w", err)
		}

		for _, change := range changes {
			result, appliedSeq, err := applyChange(tx, workspaceID, actorID, change)
			if err != nil {
				return err
			}
			if appliedSeq > serverSeq {
				serverSeq = appliedSeq
			}
			results = append(results, result)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return results, serverSeq, nil
}

// PullChanges returns changes ordered by seq after the client cursor. Any
// workspace member (viewer+) may pull.
func (r *workspaceRepository) PullChanges(ctx context.Context, workspaceID string, userID uuid.UUID, since int64, limit int) (*entity.PullResult, error) {
	if since < 0 {
		since = 0
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 1000 {
		limit = 1000
	}

	role, err := workspaceRole(r.db.WithContext(ctx), workspaceID, userID)
	if err != nil {
		return nil, err
	}
	if !role.Allows(entity.PermissionPull) {
		return nil, domainrepo.ErrMemberRequired
	}

	var rows []pullRow
	err = r.db.WithContext(ctx).Raw(
		`SELECT ec.seq, ec.entity_id, ec.rev, ec.deleted,
		        COALESCE(e.kind, '') AS kind, e.payload, e.parent_id, e.order_key
		   FROM entity_changes ec
		   LEFT JOIN entities e
		          ON e.workspace_id = ec.workspace_id AND e.id = ec.entity_id
		  WHERE ec.workspace_id = ? AND ec.seq > ?
		  ORDER BY ec.seq ASC
		  LIMIT ?`,
		workspaceID, since, limit+1,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("pull changes: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	out := &entity.PullResult{
		Changes: make([]entity.PulledChange, 0, len(rows)),
		NextSeq: since,
		HasMore: hasMore,
	}
	for _, row := range rows {
		payload := json.RawMessage(row.Payload)
		if len(payload) == 0 {
			payload = json.RawMessage("null")
		}
		out.Changes = append(out.Changes, entity.PulledChange{
			Seq:      row.Seq,
			ID:       row.EntityID,
			Kind:     row.Kind,
			Rev:      row.Rev,
			Deleted:  row.Deleted,
			Payload:  payload,
			ParentID: nullStringPtr(row.ParentID),
			OrderKey: nullStringPtr(row.OrderKey),
		})
		out.NextSeq = row.Seq
	}
	return out, nil
}

// applyChange applies one change inside the caller's transaction and returns
// the result plus the seq of the appended change (0 when nothing was applied).
func applyChange(tx *gorm.DB, workspaceID string, actorID uuid.UUID, change entity.Change) (entity.PushResult, int64, error) {
	// Idempotency: return the stored result verbatim.
	var stored []byte
	err := tx.Raw(
		`SELECT result FROM entity_ops WHERE workspace_id = ? AND op_id = ?`,
		workspaceID, change.OpID,
	).Row().Scan(&stored)
	if err == nil {
		var result entity.PushResult
		if err := json.Unmarshal(stored, &result); err != nil {
			return entity.PushResult{}, 0, fmt.Errorf("decode stored op result: %w", err)
		}
		result.OpID = change.OpID
		return result, 0, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return entity.PushResult{}, 0, fmt.Errorf("lookup op %q: %w", change.OpID, err)
	}

	var (
		curRev     int64
		curPayload []byte
		curKind    string
		curParent  sql.NullString
		curOrder   sql.NullString
		curDeleted sql.NullTime
	)
	err = tx.Raw(
		`SELECT rev, payload, kind, parent_id, order_key, deleted_at
		   FROM entities WHERE workspace_id = ? AND id = ?`,
		workspaceID, change.ID,
	).Row().Scan(&curRev, &curPayload, &curKind, &curParent, &curOrder, &curDeleted)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return entity.PushResult{}, 0, fmt.Errorf("load entity %q: %w", change.ID, err)
	}

	// Create (baseRev 0) must find no row; updates must match the current rev.
	if change.BaseRev == 0 {
		if exists {
			return storeConflict(tx, workspaceID, change, curRev, curPayload, curDeleted.Valid)
		}
	} else {
		if !exists {
			return storeConflictMissing(tx, workspaceID, change)
		}
		if curRev != change.BaseRev {
			return storeConflict(tx, workspaceID, change, curRev, curPayload, curDeleted.Valid)
		}
	}

	newRev := curRev + 1
	if change.BaseRev == 0 {
		newRev = 1
		kind := change.Kind
		if !entity.ValidKind(kind) {
			return entity.PushResult{}, 0, fmt.Errorf("invalid entity kind %q", kind)
		}
		payload, err := changePayload(change.Payload)
		if err != nil {
			return entity.PushResult{}, 0, err
		}
		if err := tx.Exec(
			`INSERT INTO entities
			   (workspace_id, id, kind, parent_id, order_key, rev, payload, updated_by, updated_at)
			 VALUES (?, ?, ?, ?, ?, 1, ?, ?, now())`,
			workspaceID, change.ID, kind, change.ParentID, change.OrderKey, payload, actorID,
		).Error; err != nil {
			return entity.PushResult{}, 0, fmt.Errorf("insert entity %q: %w", change.ID, err)
		}
	} else if change.Deleted {
		if err := tx.Exec(
			`UPDATE entities
			    SET rev = rev + 1, deleted_at = now(), updated_by = ?, updated_at = now()
			  WHERE workspace_id = ? AND id = ?`,
			actorID, workspaceID, change.ID,
		).Error; err != nil {
			return entity.PushResult{}, 0, fmt.Errorf("tombstone entity %q: %w", change.ID, err)
		}
	} else {
		kind := change.Kind
		if kind == "" {
			kind = curKind
		}
		if !entity.ValidKind(kind) {
			return entity.PushResult{}, 0, fmt.Errorf("invalid entity kind %q", kind)
		}
		payload, err := changePayload(change.Payload)
		if err != nil {
			return entity.PushResult{}, 0, err
		}
		parentID := curParent
		if change.ParentID != nil {
			parentID = sql.NullString{String: *change.ParentID, Valid: true}
		}
		orderKey := curOrder
		if change.OrderKey != nil {
			orderKey = sql.NullString{String: *change.OrderKey, Valid: true}
		}
		if err := tx.Exec(
			`UPDATE entities
			    SET kind = ?, parent_id = ?, order_key = ?, rev = rev + 1,
			        payload = ?, deleted_at = NULL, updated_by = ?, updated_at = now()
			  WHERE workspace_id = ? AND id = ?`,
			kind, nullString(parentID), nullString(orderKey), payload, actorID, workspaceID, change.ID,
		).Error; err != nil {
			return entity.PushResult{}, 0, fmt.Errorf("update entity %q: %w", change.ID, err)
		}
	}

	var seq int64
	if err := tx.Raw(
		`INSERT INTO entity_changes (workspace_id, entity_id, rev, deleted)
		 VALUES (?, ?, ?, ?) RETURNING seq`,
		workspaceID, change.ID, newRev, change.Deleted,
	).Row().Scan(&seq); err != nil {
		return entity.PushResult{}, 0, fmt.Errorf("append entity change: %w", err)
	}

	result := entity.PushResult{OpID: change.OpID, Applied: true}
	if err := storeOp(tx, workspaceID, change, result, newRev); err != nil {
		return entity.PushResult{}, 0, err
	}
	return result, seq, nil
}

func storeConflict(tx *gorm.DB, workspaceID string, change entity.Change, rev int64, payload []byte, deleted bool) (entity.PushResult, int64, error) {
	if len(payload) == 0 {
		payload = []byte("null")
	}
	result := entity.PushResult{
		OpID:     change.OpID,
		Conflict: true,
		Server: &entity.ServerState{
			Rev:     rev,
			Payload: json.RawMessage(payload),
			Deleted: deleted,
		},
	}
	if err := storeOp(tx, workspaceID, change, result, rev); err != nil {
		return entity.PushResult{}, 0, err
	}
	return result, 0, nil
}

func storeConflictMissing(tx *gorm.DB, workspaceID string, change entity.Change) (entity.PushResult, int64, error) {
	result := entity.PushResult{
		OpID:     change.OpID,
		Conflict: true,
		Reason:   "entity not found",
	}
	if err := storeOp(tx, workspaceID, change, result, 0); err != nil {
		return entity.PushResult{}, 0, err
	}
	return result, 0, nil
}

func storeOp(tx *gorm.DB, workspaceID string, change entity.Change, result entity.PushResult, rev int64) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode op result: %w", err)
	}
	if err := tx.Exec(
		`INSERT INTO entity_ops (workspace_id, op_id, entity_id, rev, result)
		 VALUES (?, ?, ?, ?, ?)`,
		workspaceID, change.OpID, change.ID, rev, encoded,
	).Error; err != nil {
		return fmt.Errorf("store op %q: %w", change.OpID, err)
	}
	return nil
}

// workspaceRole runs the membership lookup used by sync operations. A missing
// row is reported as ErrMemberRequired, never as a permission error, so
// callers can answer 404 instead of leaking workspace existence.
func workspaceRole(tx *gorm.DB, workspaceID string, userID uuid.UUID) (entity.Role, error) {
	var role string
	err := tx.Raw(
		`SELECT m.role
		   FROM workspace_members m
		   JOIN workspaces w ON w.id = m.workspace_id
		  WHERE m.workspace_id = ? AND m.user_id = ? AND w.deleted_at IS NULL`,
		workspaceID, userID,
	).Row().Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domainrepo.ErrMemberRequired
	}
	if err != nil {
		return "", fmt.Errorf("check workspace membership: %w", err)
	}
	return entity.Role(role), nil
}

func changePayload(payload json.RawMessage) ([]byte, error) {
	if len(payload) == 0 {
		return []byte("null"), nil
	}
	if !json.Valid(payload) {
		return nil, fmt.Errorf("invalid entity payload")
	}
	return payload, nil
}

func nullString(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

func nullStringPtr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

// Scan shapes. Column tags pin names because the struct field names differ
// from the SQL aliases.

type workspaceRow struct {
	ID        string       `gorm:"column:id"`
	Name      string       `gorm:"column:name"`
	OwnerID   uuid.UUID    `gorm:"column:owner_id"`
	CreatedAt time.Time    `gorm:"column:created_at"`
	DeletedAt sql.NullTime `gorm:"column:deleted_at"`
}

func (r workspaceRow) toEntity() *entity.Workspace {
	ws := &entity.Workspace{
		ID:        r.ID,
		Name:      r.Name,
		OwnerID:   r.OwnerID,
		CreatedAt: r.CreatedAt,
	}
	if r.DeletedAt.Valid {
		t := r.DeletedAt.Time
		ws.DeletedAt = &t
	}
	return ws
}

type workspaceRoleRow struct {
	ID        string       `gorm:"column:id"`
	Name      string       `gorm:"column:name"`
	OwnerID   uuid.UUID    `gorm:"column:owner_id"`
	CreatedAt time.Time    `gorm:"column:created_at"`
	DeletedAt sql.NullTime `gorm:"column:deleted_at"`
	Role      string       `gorm:"column:role"`
}

func (r workspaceRoleRow) workspaceRow() workspaceRow {
	return workspaceRow{
		ID:        r.ID,
		Name:      r.Name,
		OwnerID:   r.OwnerID,
		CreatedAt: r.CreatedAt,
		DeletedAt: r.DeletedAt,
	}
}

type memberRow struct {
	WorkspaceID string    `gorm:"column:workspace_id"`
	UserID      uuid.UUID `gorm:"column:user_id"`
	Role        string    `gorm:"column:role"`
	InvitedBy   uuid.UUID `gorm:"column:invited_by"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (r memberRow) toEntity() *entity.WorkspaceMember {
	m := &entity.WorkspaceMember{
		WorkspaceID: r.WorkspaceID,
		UserID:      r.UserID,
		Role:        entity.Role(r.Role),
		CreatedAt:   r.CreatedAt,
	}
	if r.InvitedBy != uuid.Nil {
		invited := r.InvitedBy
		m.InvitedBy = &invited
	}
	return m
}

type pullRow struct {
	Seq      int64          `gorm:"column:seq"`
	EntityID string         `gorm:"column:entity_id"`
	Rev      int64          `gorm:"column:rev"`
	Deleted  bool           `gorm:"column:deleted"`
	Kind     string         `gorm:"column:kind"`
	Payload  []byte         `gorm:"column:payload"`
	ParentID sql.NullString `gorm:"column:parent_id"`
	OrderKey sql.NullString `gorm:"column:order_key"`
}
