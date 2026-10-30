package handler

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/pkg/response"
)

// CommentHandler exposes per-entity comment threads.
type CommentHandler struct {
	uc usecase.CommentUseCase
}

// NewCommentHandler wires the comment usecase into the HTTP layer.
func NewCommentHandler(uc usecase.CommentUseCase) *CommentHandler {
	return &CommentHandler{uc: uc}
}

// commentError maps usecase errors to the API envelope. A missing entity is
// reported separately from a missing comment.
func commentError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, usecase.ErrCommentEntityNotFound):
		return response.NotFound(c, "entity not found")
	case errors.Is(err, usecase.ErrNotFound):
		return response.NotFound(c, "comment not found")
	case errors.Is(err, usecase.ErrForbidden):
		return response.Forbidden(c)
	case errors.Is(err, usecase.ErrValidation):
		return response.ValidationError(c, err.Error())
	default:
		return response.InternalError(c)
	}
}

// List returns one entity's comment feed newest-first (?entityId=&before=&limit=).
func (h *CommentHandler) List(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}

	var before *uuid.UUID
	if raw := strings.TrimSpace(c.Query("before")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return response.BadRequest(c, "invalid before cursor")
		}
		before = &parsed
	}

	comments, nextBefore, err := h.uc.List(
		c.UserContext(),
		uid,
		c.Params("id"),
		c.Query("entityId"),
		before,
		c.QueryInt("limit", 50),
	)
	if err != nil {
		return commentError(c, err)
	}

	payload := fiber.Map{"comments": comments}
	if nextBefore != "" {
		payload["nextBefore"] = nextBefore
	}
	return response.Success(c, "comments retrieved", payload)
}

// MentionsFeed returns the workspace comments that mention the caller,
// newest-first (?before=&limit=).
func (h *CommentHandler) MentionsFeed(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}

	var before *uuid.UUID
	if raw := strings.TrimSpace(c.Query("before")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return response.BadRequest(c, "invalid before cursor")
		}
		before = &parsed
	}

	comments, nextBefore, err := h.uc.ListMentions(
		c.UserContext(),
		uid,
		c.Params("id"),
		before,
		c.QueryInt("limit", 50),
	)
	if err != nil {
		return commentError(c, err)
	}
	return response.Success(c, "mentions retrieved", fiber.Map{
		"comments":   comments,
		"nextBefore": nextBefore,
	})
}

// Create posts a comment on an existing workspace entity.
func (h *CommentHandler) Create(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	var req struct {
		EntityID string      `json:"entityId"`
		Body     string      `json:"body"`
		Mentions []uuid.UUID `json:"mentions"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}

	comment, err := h.uc.Create(c.UserContext(), uid, c.Params("id"), usecase.CreateCommentInput{
		EntityID: req.EntityID,
		Body:     req.Body,
		Mentions: req.Mentions,
	})
	if err != nil {
		return commentError(c, err)
	}
	return response.Created(c, "comment created", comment)
}

// Update edits the body and/or resolves the thread (author or admin+).
func (h *CommentHandler) Update(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return response.BadRequest(c, "invalid comment id")
	}
	var req struct {
		Body     *string      `json:"body"`
		Resolved *bool        `json:"resolved"`
		Mentions *[]uuid.UUID `json:"mentions"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}

	comment, err := h.uc.Update(c.UserContext(), uid, id, usecase.UpdateCommentInput{
		Body:     req.Body,
		Resolved: req.Resolved,
		Mentions: req.Mentions,
	})
	if err != nil {
		return commentError(c, err)
	}
	return response.Success(c, "comment updated", comment)
}

// Delete removes a comment (author or admin+).
func (h *CommentHandler) Delete(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return response.BadRequest(c, "invalid comment id")
	}
	if err := h.uc.Delete(c.UserContext(), uid, id); err != nil {
		return commentError(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}
