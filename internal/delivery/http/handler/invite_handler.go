package handler

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/pkg/response"
)

// InviteHandler exposes invite creation, public metadata and acceptance.
type InviteHandler struct {
	uc usecase.InviteUseCase
}

// NewInviteHandler wires the invite usecase into the HTTP layer.
func NewInviteHandler(uc usecase.InviteUseCase) *InviteHandler {
	return &InviteHandler{uc: uc}
}

// inviteError maps invite failures to the existing response helpers. A token
// that does not resolve is always 404, never 403, so invite existence is not
// leaked.
func inviteError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, usecase.ErrNotFound):
		return response.NotFound(c, "invite not found")
	case errors.Is(err, usecase.ErrInviteExpired):
		return response.Conflict(c, "invite expired")
	case errors.Is(err, usecase.ErrInviteUsed):
		return response.Conflict(c, "invite already accepted")
	case errors.Is(err, usecase.ErrAlreadyMember):
		return response.Conflict(c, "user is already a member")
	case errors.Is(err, usecase.ErrForbidden):
		return response.Forbidden(c)
	case errors.Is(err, usecase.ErrValidation):
		return response.ValidationError(c, err.Error())
	default:
		return response.InternalError(c)
	}
}

// Create issues a single-use invite for a workspace (admin+).
func (h *InviteHandler) Create(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}

	var req struct {
		Role           string `json:"role"`
		ExpiresInHours int    `json:"expiresInHours"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	if req.ExpiresInHours < 0 {
		return response.ValidationError(c, "expiresInHours must be positive")
	}
	if req.ExpiresInHours > int(usecase.MaxInviteTTL.Hours()) {
		return response.ValidationError(c, "expiresInHours must be at most 720")
	}
	if req.ExpiresInHours == 0 {
		req.ExpiresInHours = int(usecase.DefaultInviteTTL.Hours())
	}

	token, expiresAt, err := h.uc.CreateInvite(
		c.UserContext(),
		uid,
		c.Params("id"),
		entity.Role(req.Role),
		time.Duration(req.ExpiresInHours)*time.Hour,
	)
	if err != nil {
		return inviteError(c, err)
	}

	acceptPath := "/invites/" + token + "/accept"
	return response.Created(c, "invite created", fiber.Map{
		"token":      token,
		"expiresAt":  expiresAt,
		"acceptPath": acceptPath,
	})
}

// Get returns public invite metadata (workspace name, role, expiry). No auth:
// possession of the token is the capability, and the response echoes no
// secret other than what the caller already supplied.
func (h *InviteHandler) Get(c *fiber.Ctx) error {
	info, err := h.uc.GetInvite(c.UserContext(), c.Params("token"))
	if err != nil {
		return inviteError(c, err)
	}
	return response.Success(c, "invite retrieved", info)
}

// Accept consumes the invite for the authenticated user.
func (h *InviteHandler) Accept(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	workspace, role, err := h.uc.AcceptInvite(c.UserContext(), uid, c.Params("token"))
	if err != nil {
		return inviteError(c, err)
	}
	return response.Success(c, "invite accepted", fiber.Map{
		"workspace": workspace,
		"role":      role,
	})
}
