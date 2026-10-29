package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/pkg/response"
	"github.com/abdullahPrasetio/relay-api/pkg/validator"
)

type UserHandler struct {
	uc  usecase.UserUseCase
	val *validator.Validator
}

func NewUserHandler(uc usecase.UserUseCase, val *validator.Validator) *UserHandler {
	return &UserHandler{uc: uc, val: val}
}

func (h *UserHandler) GetUser(c *fiber.Ctx) error {
	id := c.Params("id")
	user, err := h.uc.GetUser(c.UserContext(), id)
	if err != nil {
		return h.mapError(c, err)
	}
	return response.Success(c, "user retrieved", user)
}

func (h *UserHandler) ListUsers(c *fiber.Ctx) error {
	users, err := h.uc.ListUsers(c.UserContext())
	if err != nil {
		return response.InternalError(c)
	}
	return response.Success(c, "users retrieved", users)
}

func (h *UserHandler) CreateUser(c *fiber.Ctx) error {
	var req usecase.CreateUserRequest
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	if err := h.val.Validate(&req); err != nil {
		return response.BadRequest(c, err.Error())
	}

	user, err := h.uc.CreateUser(c.UserContext(), &req)
	if err != nil {
		return h.mapError(c, err)
	}
	return response.Created(c, "user created", user)
}

func (h *UserHandler) UpdateUser(c *fiber.Ctx) error {
	id := c.Params("id")

	var req usecase.UpdateUserRequest
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "invalid request body")
	}
	if err := h.val.Validate(&req); err != nil {
		return response.BadRequest(c, err.Error())
	}

	user, err := h.uc.UpdateUser(c.UserContext(), id, &req)
	if err != nil {
		return h.mapError(c, err)
	}
	return response.Success(c, "user updated", user)
}

func (h *UserHandler) DeleteUser(c *fiber.Ctx) error {
	id := c.Params("id")
	if err := h.uc.DeleteUser(c.UserContext(), id); err != nil {
		return h.mapError(c, err)
	}
	return response.Success(c, "user deleted", nil)
}

func (h *UserHandler) mapError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, usecase.ErrNotFound):
		return response.NotFound(c, "user not found")
	case errors.Is(err, usecase.ErrEmailConflict):
		return response.Error(c, fiber.StatusConflict, response.ErrConflict, "email already in use")
	case errors.Is(err, usecase.ErrInvalidUUID):
		return response.BadRequest(c, "invalid id format")
	default:
		return response.InternalError(c)
	}
}
