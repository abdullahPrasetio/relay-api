package usecase

import (
	"testing"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
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
