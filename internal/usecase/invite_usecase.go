package usecase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

var (
	// ErrInviteExpired means the invite deadline has passed.
	ErrInviteExpired = errors.New("invite expired")
	// ErrInviteUsed means the invite has already been accepted once.
	ErrInviteUsed = errors.New("invite already accepted")
)

const (
	// DefaultInviteTTL is used when the caller does not specify a duration.
	DefaultInviteTTL = 72 * time.Hour
	// MaxInviteTTL caps how far in the future an invite may point.
	MaxInviteTTL = 30 * 24 * time.Hour
	// inviteTokenBytes is the entropy of a raw invite token.
	inviteTokenBytes = 32
)

// InviteUseCase creates and accepts single-use workspace invites and exposes
// the authenticated audit feed.
//
// Invites are bearer secrets: the raw token is returned only by CreateInvite;
// only its SHA-256 hash is stored. Acceptance is single-use and atomic.
type InviteUseCase interface {
	CreateInvite(ctx context.Context, actorID uuid.UUID, workspaceID string, role entity.Role, ttl time.Duration) (string, time.Time, error)
	GetInvite(ctx context.Context, token string) (*entity.InviteInfo, error)
	AcceptInvite(ctx context.Context, userID uuid.UUID, token string) (*entity.Workspace, entity.Role, error)
}

type inviteUseCase struct {
	invites    domainrepo.InviteRepository
	workspaces domainrepo.WorkspaceRepository
}

// NewInviteUseCase wires the invite and workspace repositories.
func NewInviteUseCase(invites domainrepo.InviteRepository, workspaces domainrepo.WorkspaceRepository) InviteUseCase {
	return &inviteUseCase{invites: invites, workspaces: workspaces}
}

func (u *inviteUseCase) CreateInvite(ctx context.Context, actorID uuid.UUID, workspaceID string, role entity.Role, ttl time.Duration) (string, time.Time, error) {
	if workspaceID == "" || actorID == uuid.Nil {
		return "", time.Time{}, fmt.Errorf("%w: workspace id and actor id are required", ErrValidation)
	}
	if ttl <= 0 {
		ttl = DefaultInviteTTL
	}
	if ttl > MaxInviteTTL {
		return "", time.Time{}, fmt.Errorf("%w: invite ttl must be at most %s", ErrValidation, MaxInviteTTL)
	}

	// Admin+ only, and the role matrix decides whether this actor may grant
	// the requested role (only the owner may grant admin).
	member, err := u.workspaces.GetMember(ctx, workspaceID, actorID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", time.Time{}, ErrNotFound
		}
		return "", time.Time{}, err
	}
	if !member.Role.Allows(entity.PermissionInvite) {
		return "", time.Time{}, fmt.Errorf("%w: role %q cannot invite", ErrForbidden, member.Role)
	}
	if err := validateInvitableRole(role, member.Role); err != nil {
		return "", time.Time{}, err
	}

	rawToken, tokenHash, err := newInviteToken()
	if err != nil {
		return "", time.Time{}, err
	}

	invite := &entity.Invite{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		Role:        role,
		TokenHash:   tokenHash,
		CreatedBy:   actorID,
		ExpiresAt:   time.Now().UTC().Add(ttl),
	}
	if err := u.invites.CreateInvite(ctx, invite); err != nil {
		return "", time.Time{}, err
	}
	return rawToken, invite.ExpiresAt, nil
}

func (u *inviteUseCase) GetInvite(ctx context.Context, token string) (*entity.InviteInfo, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("%w: invite token is required", ErrValidation)
	}
	info, err := u.invites.GetInviteInfo(ctx, hashInviteToken(token))
	if err != nil {
		return nil, mapInviteError(err)
	}
	return info, nil
}

func (u *inviteUseCase) AcceptInvite(ctx context.Context, userID uuid.UUID, token string) (*entity.Workspace, entity.Role, error) {
	token = strings.TrimSpace(token)
	if userID == uuid.Nil {
		return nil, "", fmt.Errorf("%w: user id is required", ErrValidation)
	}
	if token == "" {
		return nil, "", fmt.Errorf("%w: invite token is required", ErrValidation)
	}

	workspace, role, err := u.invites.AcceptInvite(ctx, hashInviteToken(token), userID)
	if err != nil {
		return nil, "", mapInviteError(err)
	}
	return workspace, role, nil
}

func mapInviteError(err error) error {
	switch {
	case errors.Is(err, domainrepo.ErrInviteNotFound):
		return ErrNotFound
	case errors.Is(err, domainrepo.ErrInviteExpired):
		return ErrInviteExpired
	case errors.Is(err, domainrepo.ErrInviteUsed):
		return ErrInviteUsed
	case errors.Is(err, domainrepo.ErrInviteAlreadyMember):
		return ErrAlreadyMember
	default:
		return err
	}
}

// newInviteToken returns a fresh 32-byte token (hex encoded) and its SHA-256
// hex digest. Only the digest is persisted.
func newInviteToken() (raw string, hash string, err error) {
	buf := make([]byte, inviteTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate invite token: %w", err)
	}
	raw = hex.EncodeToString(buf)
	return raw, hashInviteToken(raw), nil
}

// hashInviteToken is deterministic so lookups never need the raw token at
// rest.
func hashInviteToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
