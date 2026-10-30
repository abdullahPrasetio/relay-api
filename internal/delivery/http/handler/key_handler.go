package handler

import (
	"encoding/base64"
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/pkg/response"
)

// KeyHandler exposes the per-device key registry and the workspace keys
// wrapped for each device (phase 4.1).
type KeyHandler struct {
	uc usecase.KeyUseCase
}

// NewKeyHandler wires the key usecase into the HTTP layer.
func NewKeyHandler(uc usecase.KeyUseCase) *KeyHandler {
	return &KeyHandler{uc: uc}
}

// deviceKeyError maps usecase errors to the API envelope for the caller's own
// device endpoints.
func deviceKeyError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, usecase.ErrNotFound):
		return response.NotFound(c, "device key not found")
	case errors.Is(err, usecase.ErrForbidden):
		return response.Forbidden(c)
	case errors.Is(err, usecase.ErrValidation):
		return response.ValidationError(c, err.Error())
	default:
		return response.InternalError(c)
	}
}

// workspaceKeyError maps usecase errors for the workspace-scoped key
// endpoints. A non-member gets 404 so workspace existence is not leaked.
func workspaceKeyError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, usecase.ErrNotFound):
		return response.NotFound(c, "workspace not found")
	case errors.Is(err, usecase.ErrForbidden):
		return response.Forbidden(c)
	case errors.Is(err, usecase.ErrValidation):
		return response.ValidationError(c, err.Error())
	case errors.Is(err, usecase.ErrConflict):
		return response.Conflict(c, "workspace key version already exists")
	default:
		return response.InternalError(c)
	}
}

// Register handles PUT /devices/keys: register (or reactivate) the caller's
// device public key.
func (h *KeyHandler) Register(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	var req struct {
		PublicKey string `json:"publicKey"`
		Label     string `json:"label"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	key, err := h.uc.RegisterDeviceKey(c.UserContext(), uid, req.PublicKey, req.Label)
	if err != nil {
		return deviceKeyError(c, err)
	}
	return response.Success(c, "device key registered", key)
}

// List handles GET /devices/keys: the caller's own keys, active and revoked.
func (h *KeyHandler) List(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	keys, err := h.uc.ListDeviceKeys(c.UserContext(), uid)
	if err != nil {
		return deviceKeyError(c, err)
	}
	return response.Success(c, "device keys retrieved", keys)
}

// Revoke handles DELETE /devices/keys/:id. Revoking is idempotent and only
// affects the caller's own key; anything else is 404.
func (h *KeyHandler) Revoke(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	keyID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return response.BadRequest(c, "invalid device key id")
	}
	if err := h.uc.RevokeDeviceKey(c.UserContext(), uid, keyID); err != nil {
		return deviceKeyError(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// ListWorkspaceDeviceKeys handles GET /workspaces/:id/device-keys: the active
// recipient keys of every workspace member.
func (h *KeyHandler) ListWorkspaceDeviceKeys(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	keys, err := h.uc.ListWorkspaceDeviceKeys(c.UserContext(), uid, c.Params("id"))
	if err != nil {
		return workspaceKeyError(c, err)
	}
	return response.Success(c, "device keys retrieved", keys)
}

// PutWorkspaceKeys handles POST /workspaces/:id/keys: store one wrapped
// workspace key per recipient device for the given version (admin+).
func (h *KeyHandler) PutWorkspaceKeys(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	var req struct {
		Version int `json:"version"`
		Entries []struct {
			DeviceKeyID uuid.UUID `json:"deviceKeyId"`
			WrappedKey  string    `json:"wrappedKey"`
		} `json:"entries"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}

	input := usecase.PutWorkspaceKeysInput{
		Version: req.Version,
		Entries: make([]usecase.WrappedKeyInput, 0, len(req.Entries)),
	}
	for _, entry := range req.Entries {
		input.Entries = append(input.Entries, usecase.WrappedKeyInput{
			DeviceKeyID: entry.DeviceKeyID,
			WrappedKey:  entry.WrappedKey,
		})
	}

	version, err := h.uc.PutWorkspaceKeys(c.UserContext(), uid, c.Params("id"), input)
	if err != nil {
		return workspaceKeyError(c, err)
	}
	return response.Success(c, "workspace keys stored", version)
}

// wrappedWorkspaceKey is the wire shape of one wrapped key. The envelope is
// base64 so it survives JSON unchanged.
type wrappedWorkspaceKey struct {
	DeviceKeyID uuid.UUID `json:"deviceKeyId"`
	Version     int       `json:"version"`
	WrappedKey  string    `json:"wrappedKey"`
}

// ListMyWorkspaceKeys handles GET /workspaces/:id/keys: only the caller's own
// wrapped keys, newest version first.
func (h *KeyHandler) ListMyWorkspaceKeys(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	keys, err := h.uc.ListMyWorkspaceKeys(c.UserContext(), uid, c.Params("id"))
	if err != nil {
		return workspaceKeyError(c, err)
	}

	out := make([]wrappedWorkspaceKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, wrappedWorkspaceKey{
			DeviceKeyID: key.DeviceKeyID,
			Version:     key.Version,
			WrappedKey:  base64.StdEncoding.EncodeToString(key.WrappedKey),
		})
	}
	return response.Success(c, "workspace keys retrieved", out)
}

// RotateWorkspaceKey handles POST /workspaces/:id/keys/rotate: allocate the
// next version (admin+). It never reuses or overwrites a version.
func (h *KeyHandler) RotateWorkspaceKey(c *fiber.Ctx) error {
	uid, ok := callerID(c)
	if !ok {
		return response.Unauthorized(c)
	}
	version, err := h.uc.RotateWorkspaceKey(c.UserContext(), uid, c.Params("id"))
	if err != nil {
		return workspaceKeyError(c, err)
	}
	return response.Created(c, "workspace key version created", version)
}
