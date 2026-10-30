package handler

import (
	"encoding/json"
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/pkg/response"
)

// MonitorHandler exposes monitor CRUD, run history and manual execution.
type MonitorHandler struct {
	uc usecase.MonitorUseCase
}

// NewMonitorHandler wires the monitor usecase into the HTTP layer.
func NewMonitorHandler(uc usecase.MonitorUseCase) *MonitorHandler {
	return &MonitorHandler{uc: uc}
}

// monitorError maps usecase errors to the API envelope without leaking
// monitor existence across workspaces.
func monitorError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, usecase.ErrNotFound):
		return response.NotFound(c, "monitor not found")
	case errors.Is(err, usecase.ErrForbidden):
		return response.Forbidden(c)
	case errors.Is(err, usecase.ErrValidation):
		return response.ValidationError(c, err.Error())
	case errors.Is(err, usecase.ErrConflict):
		return response.Conflict(c, "monitor name already exists")
	default:
		return response.InternalError(c)
	}
}

func (h *MonitorHandler) List(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	monitors, err := h.uc.List(c.UserContext(), uid, c.Params("id"))
	if err != nil {
		return monitorError(c, err)
	}
	return response.Success(c, "monitors retrieved", monitors)
}

func (h *MonitorHandler) Get(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return response.BadRequest(c, "invalid monitor id")
	}
	entry, err := h.uc.Get(c.UserContext(), uid, id)
	if err != nil {
		return monitorError(c, err)
	}
	return response.Success(c, "monitor retrieved", entry)
}

func (h *MonitorHandler) Create(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	var req struct {
		Name         string  `json:"name"`
		RequestID    string  `json:"requestId"`
		Cron         string  `json:"cron"`
		Enabled      *bool   `json:"enabled"`
		AlertWebhook *string `json:"alertWebhook"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	entry, err := h.uc.Create(c.UserContext(), uid, c.Params("id"), usecase.CreateMonitorInput{
		Name:         req.Name,
		RequestID:    req.RequestID,
		Cron:         req.Cron,
		Enabled:      req.Enabled,
		AlertWebhook: req.AlertWebhook,
	})
	if err != nil {
		return monitorError(c, err)
	}
	return response.Created(c, "monitor created", entry)
}

func (h *MonitorHandler) Update(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return response.BadRequest(c, "invalid monitor id")
	}
	var req struct {
		Name         *string         `json:"name"`
		RequestID    *string         `json:"requestId"`
		Cron         *string         `json:"cron"`
		Enabled      *bool           `json:"enabled"`
		AlertWebhook json.RawMessage `json:"alertWebhook"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}

	input := usecase.UpdateMonitorInput{
		Name:      req.Name,
		RequestID: req.RequestID,
		Cron:      req.Cron,
		Enabled:   req.Enabled,
	}
	if req.AlertWebhook != nil {
		input.AlertWebhookSet = true
		if string(req.AlertWebhook) != "null" {
			var webhook string
			if err := json.Unmarshal(req.AlertWebhook, &webhook); err != nil {
				return response.BadRequest(c, "invalid alertWebhook")
			}
			input.AlertWebhook = &webhook
		}
	}

	entry, err := h.uc.Update(c.UserContext(), uid, id, input)
	if err != nil {
		return monitorError(c, err)
	}
	return response.Success(c, "monitor updated", entry)
}

func (h *MonitorHandler) Delete(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return response.BadRequest(c, "invalid monitor id")
	}
	if err := h.uc.Delete(c.UserContext(), uid, id); err != nil {
		return monitorError(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *MonitorHandler) ListRuns(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return response.BadRequest(c, "invalid monitor id")
	}
	runs, err := h.uc.ListRuns(c.UserContext(), uid, id, c.QueryInt("limit", 50))
	if err != nil {
		return monitorError(c, err)
	}
	return response.Success(c, "monitor runs retrieved", runs)
}

// Run executes the monitor synchronously and returns the stored run. A runner
// failure is a recorded failed run (HTTP 200 with ok=false), not a success.
func (h *MonitorHandler) Run(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return response.BadRequest(c, "invalid monitor id")
	}
	run, err := h.uc.RunNow(c.UserContext(), uid, id)
	if err != nil {
		return monitorError(c, err)
	}
	return response.Success(c, "monitor run completed", run)
}
