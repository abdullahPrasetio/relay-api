package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

// stubWorkspaceRepo embeds the interface so only GetMember needs a body.
type stubWorkspaceRepo struct {
	domainrepo.WorkspaceRepository
	member *entity.WorkspaceMember
	err    error
}

func (s stubWorkspaceRepo) GetMember(context.Context, string, uuid.UUID) (*entity.WorkspaceMember, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.member, nil
}

type stubInviteRepo struct {
	domainrepo.InviteRepository
	created   *entity.Invite
	createErr error

	info    *entity.InviteInfo
	infoErr error

	acceptWS   *entity.Workspace
	acceptRole entity.Role
	acceptErr  error

	lastHash string
}

func (s *stubInviteRepo) CreateInvite(_ context.Context, invite *entity.Invite) error {
	s.created = invite
	return s.createErr
}

func (s *stubInviteRepo) GetInviteInfo(_ context.Context, tokenHash string) (*entity.InviteInfo, error) {
	s.lastHash = tokenHash
	if s.infoErr != nil {
		return nil, s.infoErr
	}
	return s.info, nil
}

func (s *stubInviteRepo) AcceptInvite(_ context.Context, tokenHash string, _ uuid.UUID) (*entity.Workspace, entity.Role, error) {
	s.lastHash = tokenHash
	if s.acceptErr != nil {
		return nil, "", s.acceptErr
	}
	return s.acceptWS, s.acceptRole, nil
}

func TestNewInviteToken(t *testing.T) {
	raw, hash, err := newInviteToken()
	if err != nil {
		t.Fatalf("newInviteToken: %v", err)
	}
	if len(raw) != 64 {
		t.Fatalf("raw token length = %d, want 64 hex chars", len(raw))
	}
	if _, err := hex.DecodeString(raw); err != nil {
		t.Fatalf("raw token is not hex: %v", err)
	}
	sum := sha256.Sum256([]byte(raw))
	if want := hex.EncodeToString(sum[:]); hash != want {
		t.Fatalf("hash = %q, want sha256 hex %q", hash, want)
	}
	if hash == raw {
		t.Fatal("stored hash must never equal the raw token")
	}

	raw2, hash2, err := newInviteToken()
	if err != nil {
		t.Fatalf("newInviteToken second: %v", err)
	}
	if raw == raw2 || hash == hash2 {
		t.Fatal("two invites must not share a token")
	}
	if hashInviteToken(raw2) != hash2 {
		t.Fatal("hashInviteToken is not deterministic")
	}
}

func TestCreateInviteRoleRules(t *testing.T) {
	wsID := "ws_test"
	actor := uuid.MustParse("11111111-1111-4111-8111-111111111111")

	tests := []struct {
		name      string
		actorRole entity.Role
		role      entity.Role
		memberErr error
		ttl       time.Duration
		wantErr   error
	}{
		{"owner grants admin", entity.RoleOwner, entity.RoleAdmin, nil, time.Hour, nil},
		{"owner grants editor", entity.RoleOwner, entity.RoleEditor, nil, time.Hour, nil},
		{"owner grants viewer", entity.RoleOwner, entity.RoleViewer, nil, time.Hour, nil},
		{"admin grants editor", entity.RoleAdmin, entity.RoleEditor, nil, time.Hour, nil},
		{"admin cannot grant admin", entity.RoleAdmin, entity.RoleAdmin, nil, time.Hour, ErrForbidden},
		{"editor cannot invite", entity.RoleEditor, entity.RoleViewer, nil, time.Hour, ErrForbidden},
		{"viewer cannot invite", entity.RoleViewer, entity.RoleViewer, nil, time.Hour, ErrForbidden},
		{"owner role is never invitable", entity.RoleOwner, entity.RoleOwner, nil, time.Hour, ErrValidation},
		{"unknown role rejected", entity.RoleOwner, entity.Role("superuser"), nil, time.Hour, ErrValidation},
		{"non-member is 404", "", "", gorm.ErrRecordNotFound, time.Hour, ErrNotFound},
		{"ttl above max rejected", entity.RoleOwner, entity.RoleViewer, nil, MaxInviteTTL + time.Second, ErrValidation},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invites := &stubInviteRepo{}
			member := &entity.WorkspaceMember{WorkspaceID: wsID, UserID: actor, Role: tt.actorRole}
			uc := NewInviteUseCase(invites, stubWorkspaceRepo{member: member, err: tt.memberErr})

			token, _, err := uc.CreateInvite(context.Background(), actor, wsID, tt.role, tt.ttl)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				if invites.created != nil {
					t.Fatal("invite must not be persisted on a rejected request")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(token) != 64 {
				t.Fatalf("token length = %d, want 64", len(token))
			}
			if invites.created == nil {
				t.Fatal("invite was not persisted")
			}
			if invites.created.TokenHash == token {
				t.Fatal("raw token must not be stored")
			}
			if invites.created.TokenHash != hashInviteToken(token) {
				t.Fatal("stored hash does not match the returned token")
			}
			if invites.created.Role != tt.role {
				t.Fatalf("role = %q, want %q", invites.created.Role, tt.role)
			}
			if invites.created.CreatedBy != actor {
				t.Fatalf("createdBy = %s, want %s", invites.created.CreatedBy, actor)
			}
		})
	}
}

func TestCreateInviteDefaultTTL(t *testing.T) {
	invites := &stubInviteRepo{}
	actor := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	uc := NewInviteUseCase(invites, stubWorkspaceRepo{member: &entity.WorkspaceMember{
		WorkspaceID: "ws_test", UserID: actor, Role: entity.RoleOwner,
	}})

	_, expiresAt, err := uc.CreateInvite(context.Background(), actor, "ws_test", entity.RoleViewer, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := time.Until(expiresAt); got > DefaultInviteTTL || got < DefaultInviteTTL-time.Minute {
		t.Fatalf("expiry = %s from now, want about %s", got, DefaultInviteTTL)
	}
}

func TestGetInviteHashesToken(t *testing.T) {
	info := &entity.InviteInfo{WorkspaceID: "ws_test", WorkspaceName: "Team", Role: entity.RoleEditor}
	repo := &stubInviteRepo{info: info}
	uc := NewInviteUseCase(repo, stubWorkspaceRepo{})

	got, err := uc.GetInvite(context.Background(), "  raw-token  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != info {
		t.Fatal("unexpected invite info")
	}
	if repo.lastHash != hashInviteToken("raw-token") {
		t.Fatalf("lookup hash = %q, want sha256 of the trimmed token", repo.lastHash)
	}

	repo.infoErr = domainrepo.ErrInviteUsed
	if _, err := uc.GetInvite(context.Background(), "raw-token"); !errors.Is(err, ErrInviteUsed) {
		t.Fatalf("error = %v, want ErrInviteUsed", err)
	}
}

func TestAcceptInviteValidationAndErrors(t *testing.T) {
	userID := uuid.MustParse("22222222-2222-4222-8222-222222222222")

	uc := NewInviteUseCase(&stubInviteRepo{}, stubWorkspaceRepo{})
	if _, _, err := uc.AcceptInvite(context.Background(), userID, "   "); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty token error = %v, want ErrValidation", err)
	}
	if _, _, err := uc.AcceptInvite(context.Background(), uuid.Nil, "token"); !errors.Is(err, ErrValidation) {
		t.Fatalf("nil user error = %v, want ErrValidation", err)
	}

	cases := []struct {
		name    string
		repoErr error
		want    error
	}{
		{"missing", domainrepo.ErrInviteNotFound, ErrNotFound},
		{"expired", domainrepo.ErrInviteExpired, ErrInviteExpired},
		{"used", domainrepo.ErrInviteUsed, ErrInviteUsed},
		{"already member", domainrepo.ErrInviteAlreadyMember, ErrAlreadyMember},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo := &stubInviteRepo{acceptErr: tt.repoErr}
			uc := NewInviteUseCase(repo, stubWorkspaceRepo{})
			_, _, err := uc.AcceptInvite(context.Background(), userID, "token")
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if repo.lastHash != hashInviteToken("token") {
				t.Fatalf("repo received %q, want hashed token", repo.lastHash)
			}
		})
	}
}
