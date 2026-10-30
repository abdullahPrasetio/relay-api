package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
	"github.com/abdullahPrasetio/relay-api/pkg/response"
)

// WorkspaceHandler exposes workspace CRUD, membership and delta sync.
type WorkspaceHandler struct {
	uc usecase.WorkspaceUseCase
}

// NewWorkspaceHandler wires the workspace usecase into the HTTP layer.
func NewWorkspaceHandler(uc usecase.WorkspaceUseCase) *WorkspaceHandler {
	return &WorkspaceHandler{uc: uc}
}

// callerID resolves the authenticated user from the JWT claims.
func callerID(c *fiber.Ctx) (uuid.UUID, bool) {
	claims := auth.GetClaims(c)
	if claims == nil {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// workspaceError maps usecase errors to the API envelope. A missing workspace
// and a workspace the caller cannot see both return 404 so membership is not
// leaked.
func workspaceError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, usecase.ErrNotFound):
		return response.NotFound(c, "workspace not found")
	case errors.Is(err, usecase.ErrForbidden):
		return response.Forbidden(c)
	case errors.Is(err, usecase.ErrValidation):
		return response.ValidationError(c, err.Error())
	case errors.Is(err, usecase.ErrAlreadyMember):
		return response.Conflict(c, "user is already a member")
	default:
		return response.InternalError(c)
	}
}

func (h *WorkspaceHandler) Create(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	ws, err := h.uc.CreateWorkspace(c.UserContext(), uid, req.Name)
	if err != nil {
		return workspaceError(c, err)
	}
	return response.Created(c, "workspace created", ws)
}

func (h *WorkspaceHandler) List(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	list, err := h.uc.ListWorkspaces(c.UserContext(), uid)
	if err != nil {
		return workspaceError(c, err)
	}
	return response.Success(c, "workspaces retrieved", list)
}

func (h *WorkspaceHandler) Get(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	ws, err := h.uc.GetWorkspace(c.UserContext(), uid, c.Params("id"))
	if err != nil {
		return workspaceError(c, err)
	}
	return response.Success(c, "workspace retrieved", ws)
}

func (h *WorkspaceHandler) Rename(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	ws, err := h.uc.RenameWorkspace(c.UserContext(), uid, c.Params("id"), req.Name)
	if err != nil {
		return workspaceError(c, err)
	}
	return response.Success(c, "workspace renamed", ws)
}

func (h *WorkspaceHandler) Delete(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	if err := h.uc.DeleteWorkspace(c.UserContext(), uid, c.Params("id")); err != nil {
		return workspaceError(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *WorkspaceHandler) ListMembers(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	members, err := h.uc.ListMembers(c.UserContext(), uid, c.Params("id"))
	if err != nil {
		return workspaceError(c, err)
	}
	return response.Success(c, "members retrieved", members)
}

func (h *WorkspaceHandler) AddMember(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	var req struct {
		UserID uuid.UUID `json:"userId"`
		Role   string    `json:"role"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	member, err := h.uc.AddMember(c.UserContext(), uid, c.Params("id"), req.UserID, entity.Role(req.Role))
	if err != nil {
		return workspaceError(c, err)
	}
	return response.Created(c, "member added", member)
}

func (h *WorkspaceHandler) UpdateMemberRole(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	target, err := uuid.Parse(c.Params("userId"))
	if err != nil {
		return response.BadRequest(c, "invalid user id")
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	member, err := h.uc.UpdateMemberRole(c.UserContext(), uid, c.Params("id"), target, entity.Role(req.Role))
	if err != nil {
		return workspaceError(c, err)
	}
	return response.Success(c, "member updated", member)
}

func (h *WorkspaceHandler) RemoveMember(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	target, err := uuid.Parse(c.Params("userId"))
	if err != nil {
		return response.BadRequest(c, "invalid user id")
	}
	if err := h.uc.RemoveMember(c.UserContext(), uid, c.Params("id"), target); err != nil {
		return workspaceError(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// ListAudit returns the workspace audit feed (admin+), newest first.
func (h *WorkspaceHandler) ListAudit(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	events, nextBefore, err := h.uc.ListAudit(
		c.UserContext(),
		uid,
		c.Params("id"),
		int64(c.QueryInt("before", 0)),
		c.QueryInt("limit", 50),
	)
	if err != nil {
		return workspaceError(c, err)
	}
	return response.Success(c, "audit events retrieved", fiber.Map{
		"events":     events,
		"nextBefore": nextBefore,
	})
}

// Pull returns entity changes after the client cursor (?since=<seq>).
func (h *WorkspaceHandler) Pull(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	res, err := h.uc.PullChanges(
		c.UserContext(),
		c.Params("id"),
		uid,
		int64(c.QueryInt("since", 0)),
		c.QueryInt("limit", 500),
	)
	if err != nil {
		return workspaceError(c, err)
	}
	return response.Success(c, "changes retrieved", res)
}

// Push applies one batch of entity changes; each result is per-op.
func (h *WorkspaceHandler) Push(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	var req struct {
		Changes []entity.Change `json:"changes"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	results, serverSeq, err := h.uc.PushChanges(c.UserContext(), c.Params("id"), uid, req.Changes)
	if err != nil {
		return workspaceError(c, err)
	}
	return response.Success(c, "changes pushed", fiber.Map{
		"results":   results,
		"serverSeq": serverSeq,
	})
}
