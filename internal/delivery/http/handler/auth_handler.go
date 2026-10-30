package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
	"github.com/abdullahPrasetio/relay-api/pkg/response"
	"github.com/abdullahPrasetio/relay-api/pkg/validator"
)

// AuthHandler exposes register/login/refresh/logout plus device management.
// Token endpoints are public; devices and /me require auth.Middleware.
type AuthHandler struct {
	uc  usecase.AuthUseCase
	val *validator.Validator
}

func NewAuthHandler(uc usecase.AuthUseCase, val *validator.Validator) *AuthHandler {
	return &AuthHandler{uc: uc, val: val}
}

func (h *AuthHandler) Register(c *fiber.Ctx) error {
	var req usecase.RegisterRequest
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	if err := h.val.Validate(&req); err != nil {
		return response.BadRequest(c, err.Error())
	}

	user, err := h.uc.Register(c.UserContext(), &req)
	if err != nil {
		switch {
		case errors.Is(err, usecase.ErrEmailConflict):
			return response.Conflict(c, "email already in use")
		case errors.Is(err, usecase.ErrInvalidInput):
			return response.BadRequest(c, "invalid registration data")
		default:
			return response.InternalError(c)
		}
	}
	return response.Created(c, "user registered", user)
}

func (h *AuthHandler) Login(c *fiber.Ctx) error {
	var req usecase.LoginRequest
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	if err := h.val.Validate(&req); err != nil {
		return response.BadRequest(c, err.Error())
	}

	tokens, err := h.uc.Login(c.UserContext(), &req)
	if err != nil {
		if errors.Is(err, usecase.ErrInvalidCredentials) {
			return response.Error(c, fiber.StatusUnauthorized, response.ErrUnauthorized, "invalid email or password")
		}
		return response.InternalError(c)
	}
	return response.Success(c, "login successful", tokens)
}

func (h *AuthHandler) Refresh(c *fiber.Ctx) error {
	var req usecase.RefreshRequest
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	if err := h.val.Validate(&req); err != nil {
		return response.BadRequest(c, err.Error())
	}

	tokens, err := h.uc.Refresh(c.UserContext(), &req)
	if err != nil {
		if errors.Is(err, usecase.ErrInvalidRefresh) {
			return response.Error(c, fiber.StatusUnauthorized, response.ErrUnauthorized, "invalid refresh token")
		}
		return response.InternalError(c)
	}
	return response.Success(c, "token refreshed", tokens)
}

func (h *AuthHandler) Logout(c *fiber.Ctx) error {
	var req usecase.RefreshRequest
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	if err := h.val.Validate(&req); err != nil {
		return response.BadRequest(c, err.Error())
	}

	if err := h.uc.Logout(c.UserContext(), req.RefreshToken); err != nil {
		return response.InternalError(c)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *AuthHandler) ListDevices(c *fiber.Ctx) error {
	userID, ok := subject(c)
	if !ok {
		return response.Unauthorized(c)
	}
	devices, err := h.uc.ListDevices(c.UserContext(), userID)
	if err != nil {
		if errors.Is(err, usecase.ErrInvalidUUID) {
			return response.Unauthorized(c)
		}
		return response.InternalError(c)
	}
	return response.Success(c, "devices retrieved", devices)
}

func (h *AuthHandler) RevokeDevice(c *fiber.Ctx) error {
	userID, ok := subject(c)
	if !ok {
		return response.Unauthorized(c)
	}
	if err := h.uc.RevokeDevice(c.UserContext(), userID, c.Params("id")); err != nil {
		switch {
		case errors.Is(err, usecase.ErrNotFound):
			return response.NotFound(c, "device not found")
		case errors.Is(err, usecase.ErrInvalidUUID):
			return response.BadRequest(c, "invalid device id")
		default:
			return response.InternalError(c)
		}
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *AuthHandler) Me(c *fiber.Ctx) error {
	userID, ok := subject(c)
	if !ok {
		return response.Unauthorized(c)
	}
	user, err := h.uc.Me(c.UserContext(), userID)
	if err != nil {
		switch {
		case errors.Is(err, usecase.ErrNotFound):
			return response.NotFound(c, "user not found")
		case errors.Is(err, usecase.ErrInvalidUUID):
			return response.Unauthorized(c)
		default:
			return response.InternalError(c)
		}
	}
	return response.Success(c, "current user", user)
}

// subject reads the user id from the JWT installed by auth.Middleware.
func subject(c *fiber.Ctx) (string, bool) {
	claims := auth.GetClaims(c)
	if claims == nil || claims.Subject == "" {
		return "", false
	}
	return claims.Subject, true
}
