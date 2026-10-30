package usecase

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

func TestRolePermissionMatrix(t *testing.T) {
	permissions := []entity.Permission{
		entity.PermissionPull,
		entity.PermissionPush,
		entity.PermissionInvite,
		entity.PermissionManageMembers,
		entity.PermissionListMembers,
		entity.PermissionUpdateWorkspace,
		entity.PermissionDeleteWorkspace,
	}

	expected := map[entity.Role]map[entity.Permission]bool{
		entity.RoleOwner: {
			entity.PermissionPull:            true,
			entity.PermissionPush:            true,
			entity.PermissionInvite:          true,
			entity.PermissionManageMembers:   true,
			entity.PermissionListMembers:     true,
			entity.PermissionUpdateWorkspace: true,
			entity.PermissionDeleteWorkspace: true,
		},
		entity.RoleAdmin: {
			entity.PermissionPull:            true,
			entity.PermissionPush:            true,
			entity.PermissionInvite:          true,
			entity.PermissionManageMembers:   true,
			entity.PermissionListMembers:     true,
			entity.PermissionUpdateWorkspace: true,
			entity.PermissionDeleteWorkspace: false,
		},
		entity.RoleEditor: {
			entity.PermissionPull:            true,
			entity.PermissionPush:            true,
			entity.PermissionInvite:          false,
			entity.PermissionManageMembers:   false,
			entity.PermissionListMembers:     true,
			entity.PermissionUpdateWorkspace: false,
			entity.PermissionDeleteWorkspace: false,
		},
		entity.RoleViewer: {
			entity.PermissionPull:            true,
			entity.PermissionPush:            false,
			entity.PermissionInvite:          false,
			entity.PermissionManageMembers:   false,
			entity.PermissionListMembers:     true,
			entity.PermissionUpdateWorkspace: false,
			entity.PermissionDeleteWorkspace: false,
		},
	}

	for _, role := range []entity.Role{entity.RoleOwner, entity.RoleAdmin, entity.RoleEditor, entity.RoleViewer} {
		if !role.Valid() {
			t.Fatalf("role %q should be valid", role)
		}
		for _, permission := range permissions {
			got := role.Allows(permission)
			want := expected[role][permission]
			if got != want {
				t.Errorf("role %q Allows(%q) = %v, want %v", role, permission, got, want)
			}
		}
	}

	// Unknown roles get no permissions at all.
	for _, role := range []entity.Role{"", "superuser", "OWNER"} {
		if role.Valid() {
			t.Errorf("role %q must not be valid", role)
		}
		for _, permission := range permissions {
			if role.Allows(permission) {
				t.Errorf("unknown role %q must not allow %q", role, permission)
			}
		}
	}
}

func TestValidateInvitableRole(t *testing.T) {
	tests := []struct {
		name      string
		role      entity.Role
		actorRole entity.Role
		wantErr   bool
	}{
		{"owner grants admin", entity.RoleAdmin, entity.RoleOwner, false},
		{"owner grants editor", entity.RoleEditor, entity.RoleOwner, false},
		{"owner grants viewer", entity.RoleViewer, entity.RoleOwner, false},
		{"admin grants editor", entity.RoleEditor, entity.RoleAdmin, false},
		{"admin grants viewer", entity.RoleViewer, entity.RoleAdmin, false},
		{"admin cannot grant admin", entity.RoleAdmin, entity.RoleAdmin, true},
		{"cannot grant owner", entity.RoleOwner, entity.RoleOwner, true},
		{"unknown role rejected", entity.Role("guest"), entity.RoleOwner, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInvitableRole(tt.role, tt.actorRole)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateInvitableRole(%q, %q) error = %v, wantErr %v", tt.role, tt.actorRole, err, tt.wantErr)
			}
		})
	}
}

func TestValidateChange(t *testing.T) {
	valid := entity.Change{OpID: "op1", ID: "req_1", Kind: entity.KindRequest, BaseRev: 0, Payload: []byte(`{"name":"x"}`)}
	if err := validateChange(valid); err != nil {
		t.Fatalf("valid create rejected: %v", err)
	}

	folder := entity.Change{OpID: "opf", ID: "folder_1", Kind: entity.KindFolder, BaseRev: 0, Payload: []byte(`{"name":"F"}`)}
	if err := validateChange(folder); err != nil {
		t.Fatalf("valid folder create rejected: %v", err)
	}

	update := entity.Change{OpID: "op2", ID: "req_1", BaseRev: 1, Payload: []byte(`{"name":"y"}`)}
	if err := validateChange(update); err != nil {
		t.Fatalf("valid update rejected: %v", err)
	}

	deleteOp := entity.Change{OpID: "op3", ID: "req_1", BaseRev: 2, Deleted: true}
	if err := validateChange(deleteOp); err != nil {
		t.Fatalf("valid delete rejected: %v", err)
	}

	cases := []entity.Change{
		{ID: "req_1", Kind: entity.KindRequest, Payload: []byte(`{}`)},
		{OpID: "op", Kind: entity.KindRequest, Payload: []byte(`{}`)},
		{OpID: "op", ID: "req_1", Kind: "unknown_kind", BaseRev: 0, Payload: []byte(`{}`)},
		{OpID: "op", ID: "req_1", BaseRev: -1, Payload: []byte(`{}`)},
		{OpID: "op", ID: "req_1", Kind: entity.KindRequest, BaseRev: 0, Payload: []byte(`{`)},
		{OpID: "op", ID: "req_1", BaseRev: 0, Payload: []byte(`{}`)},
		{OpID: "op", ID: "req_1", BaseRev: 0, Deleted: true},
	}
	for i, change := range cases {
		if err := validateChange(change); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

// recordingWorkspaceRepo implements only PushChanges. The embedded nil
// interface panics if any other method is called, which keeps the stub small
// and proves PushChanges never falls through to the repository.
type recordingWorkspaceRepo struct {
	domainrepo.WorkspaceRepository
	pushCalls int
}

func (r *recordingWorkspaceRepo) PushChanges(_ context.Context, _ string, _ uuid.UUID, changes []entity.Change) ([]entity.PushResult, int64, error) {
	r.pushCalls++
	return make([]entity.PushResult, len(changes)), 42, nil
}

func TestPushChangesBatchCap(t *testing.T) {
	repo := &recordingWorkspaceRepo{}
	uc := NewWorkspaceUseCase(repo)
	ctx := context.Background()
	actor := uuid.New()

	batch := func(n int) []entity.Change {
		changes := make([]entity.Change, n)
		for i := range changes {
			changes[i] = entity.Change{
				OpID:    fmt.Sprintf("op-%d", i),
				ID:      fmt.Sprintf("req_%d", i),
				Kind:    entity.KindRequest,
				Payload: []byte(`{}`),
			}
		}
		return changes
	}

	results, seq, err := uc.PushChanges(ctx, "ws_1", actor, batch(MaxChangesPerPush))
	if err != nil {
		t.Fatalf("batch of %d changes rejected: %v", MaxChangesPerPush, err)
	}
	if len(results) != MaxChangesPerPush || seq != 42 {
		t.Fatalf("got %d results / seq %d, want %d / 42", len(results), seq, MaxChangesPerPush)
	}
	if repo.pushCalls != 1 {
		t.Fatalf("repository called %d times, want 1", repo.pushCalls)
	}

	results, seq, err = uc.PushChanges(ctx, "ws_1", actor, batch(MaxChangesPerPush+1))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("oversized batch error = %v, want ErrValidation", err)
	}
	if results != nil || seq != 0 {
		t.Fatalf("oversized batch returned results %v / seq %d, want nil / 0", results, seq)
	}
	if repo.pushCalls != 1 {
		t.Fatalf("repository called for oversized batch (%d calls), want 1", repo.pushCalls)
	}
}
