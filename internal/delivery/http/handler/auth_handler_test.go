package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/pkg/validator"
)

// stubAuthUC lets handler tests exercise request parsing/validation and error
// mapping without a database.
type stubAuthUC struct {
	register func(context.Context, *usecase.RegisterRequest) (*entity.User, error)
	login    func(context.Context, *usecase.LoginRequest) (*usecase.AuthTokens, error)
	refresh  func(context.Context, *usecase.RefreshRequest) (*usecase.AuthTokens, error)
	logout   func(context.Context, string) error
}

func (s *stubAuthUC) Register(ctx context.Context, r *usecase.RegisterRequest) (*entity.User, error) {
	if s.register == nil {
		return nil, errors.New("unexpected Register call")
	}
	return s.register(ctx, r)
}
func (s *stubAuthUC) Login(ctx context.Context, r *usecase.LoginRequest) (*usecase.AuthTokens, error) {
	if s.login == nil {
		return nil, errors.New("unexpected Login call")
	}
	return s.login(ctx, r)
}
func (s *stubAuthUC) Refresh(ctx context.Context, r *usecase.RefreshRequest) (*usecase.AuthTokens, error) {
	if s.refresh == nil {
		return nil, errors.New("unexpected Refresh call")
	}
	return s.refresh(ctx, r)
}
func (s *stubAuthUC) Logout(ctx context.Context, token string) error {
	if s.logout == nil {
		return nil
	}
	return s.logout(ctx, token)
}
func (s *stubAuthUC) ListDevices(context.Context, string) ([]usecase.Device, error) {
	return nil, nil
}
func (s *stubAuthUC) RevokeDevice(context.Context, string, string) error { return nil }
func (s *stubAuthUC) Me(context.Context, string) (*entity.User, error)   { return nil, nil }

func newTestApp(uc usecase.AuthUseCase) *fiber.App {
	app := fiber.New()
	h := handler.NewAuthHandler(uc, validator.New())
	app.Post("/auth/register", h.Register)
	app.Post("/auth/login", h.Login)
	app.Post("/auth/refresh", h.Refresh)
	app.Post("/auth/logout", h.Logout)
	return app
}

func postJSON(t *testing.T, app *fiber.App, path, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	return resp
}

func TestRegisterValidation(t *testing.T) {
	called := false
	app := newTestApp(&stubAuthUC{register: func(context.Context, *usecase.RegisterRequest) (*entity.User, error) {
		called = true
		return &entity.User{}, nil
	}})

	cases := []struct {
		name string
		body string
	}{
		{"malformed json", `{`},
		{"invalid email", `{"name":"Ada","email":"not-an-email","password":"password123"}`},
		{"short password", `{"name":"Ada","email":"ada@example.com","password":"short"}`},
		{"missing name", `{"email":"ada@example.com","password":"password123"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postJSON(t, app, "/auth/register", tc.body)
			assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode)
		})
	}
	assert.False(t, called, "invalid requests must not reach the usecase")
}

func TestRegisterConflict(t *testing.T) {
	app := newTestApp(&stubAuthUC{register: func(context.Context, *usecase.RegisterRequest) (*entity.User, error) {
		return nil, usecase.ErrEmailConflict
	}})
	resp := postJSON(t, app, "/auth/register", `{"name":"Ada","email":"ada@example.com","password":"password123"}`)
	assert.Equal(t, fiber.StatusConflict, resp.StatusCode)
}

func TestLoginErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"invalid credentials", usecase.ErrInvalidCredentials, fiber.StatusUnauthorized},
		{"internal", errors.New("db down"), fiber.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp(&stubAuthUC{login: func(context.Context, *usecase.LoginRequest) (*usecase.AuthTokens, error) {
				return nil, tc.err
			}})
			resp := postJSON(t, app, "/auth/login", `{"email":"ada@example.com","password":"password123"}`)
			assert.Equal(t, tc.status, resp.StatusCode)
		})
	}
}

func TestLoginValidation(t *testing.T) {
	app := newTestApp(&stubAuthUC{})
	resp := postJSON(t, app, "/auth/login", `{"email":"nope","password":""}`)
	assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode)
}

func TestLoginSuccessShape(t *testing.T) {
	app := newTestApp(&stubAuthUC{login: func(context.Context, *usecase.LoginRequest) (*usecase.AuthTokens, error) {
		return &usecase.AuthTokens{AccessToken: "access", RefreshToken: "refresh"}, nil
	}})
	resp := postJSON(t, app, "/auth/login", `{"email":"ada@example.com","password":"password123","deviceLabel":"MacBook"}`)
	require.Equal(t, fiber.StatusOK, resp.StatusCode)

	var payload struct {
		Status bool `json:"status"`
		Data   struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	assert.True(t, payload.Status)
	assert.Equal(t, "access", payload.Data.AccessToken)
	assert.Equal(t, "refresh", payload.Data.RefreshToken)
}

func TestRefreshAndLogout(t *testing.T) {
	var logoutToken string
	app := newTestApp(&stubAuthUC{
		refresh: func(context.Context, *usecase.RefreshRequest) (*usecase.AuthTokens, error) {
			return nil, usecase.ErrInvalidRefresh
		},
		logout: func(_ context.Context, token string) error {
			logoutToken = token
			return nil
		},
	})

	resp := postJSON(t, app, "/auth/refresh", `{"refreshToken":"stale"}`)
	assert.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)

	resp = postJSON(t, app, "/auth/refresh", `{}`)
	assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode)

	resp = postJSON(t, app, "/auth/logout", `{"refreshToken":"opaque"}`)
	assert.Equal(t, fiber.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "opaque", logoutToken)
}
