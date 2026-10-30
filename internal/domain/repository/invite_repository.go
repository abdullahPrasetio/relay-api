package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

var (
	// ErrInviteNotFound means the token hash does not match an invite, or its
	// workspace is gone. Callers SHOULD map this to 404.
	ErrInviteNotFound = errors.New("invite not found")
	// ErrInviteExpired means the invite deadline has passed.
	ErrInviteExpired = errors.New("invite expired")
	// ErrInviteUsed means the invite has already been accepted. Acceptance is
	// single-use, so a second accept must never grant membership.
	ErrInviteUsed = errors.New("invite already accepted")
	// ErrInviteAlreadyMember means the accepting user is already in the
	// workspace; the invite is left untouched.
	ErrInviteAlreadyMember = errors.New("already a workspace member")
)

// InviteRepository persists invites and performs the atomic accept flow.
type InviteRepository interface {
	// CreateInvite stores the invite and its creation audit event in one
	// transaction.
	CreateInvite(ctx context.Context, invite *entity.Invite) error

	// GetInviteInfo resolves an invite by token hash for the public metadata
	// endpoint. Expired and already-accepted invites are reported as errors.
	GetInviteInfo(ctx context.Context, tokenHash string) (*entity.InviteInfo, error)

	// AcceptInvite consumes the invite, inserts the membership and writes the
	// audit event in one transaction. It returns the joined workspace.
	AcceptInvite(ctx context.Context, tokenHash string, userID uuid.UUID) (*entity.Workspace, entity.Role, error)
}
