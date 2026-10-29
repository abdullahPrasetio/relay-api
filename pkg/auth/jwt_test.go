package auth_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

var testCfg = &auth.Config{
	Secret:   "super-secret-key-at-least-32-bytes!",
	Issuer:   "wapgo-test",
	Audience: "wapgo-api",
	Expiry:   time.Hour,
}

// ── Sign ────────────────────────────────────────────────────────────────────

func TestSign_OK(t *testing.T) {
	tok, err := auth.Sign("user-1", []string{"admin"}, testCfg)
	require.NoError(t, err)
	assert.NotEmpty(t, tok)
	assert.Equal(t, 2, strings.Count(tok, "."), "HS256 JWT has 3 parts separated by 2 dots")
}

func TestSign_WeakSecret(t *testing.T) {
	_, err := auth.Sign("u", nil, &auth.Config{Secret: "short"})
	assert.ErrorIs(t, err, auth.ErrWeakSecret)
}

func TestSign_SetsJTI(t *testing.T) {
	tok, err := auth.Sign("user-1", nil, testCfg)
	require.NoError(t, err)
	claims, err := auth.Verify(tok, testCfg)
	require.NoError(t, err)
	assert.NotEmpty(t, claims.ID, "Sign must embed a non-empty JTI for blacklisting")
}

func TestSign_JTIIsUnique(t *testing.T) {
	tok1, _ := auth.Sign("user-1", nil, testCfg)
	tok2, _ := auth.Sign("user-1", nil, testCfg)
	c1, _ := auth.Verify(tok1, testCfg)
	c2, _ := auth.Verify(tok2, testCfg)
	assert.NotEqual(t, c1.ID, c2.ID, "each Sign call must produce a unique JTI")
}

func TestSign_SetsTokenTypeAccess(t *testing.T) {
	tok, _ := auth.Sign("user-1", nil, testCfg)
	claims, err := auth.Verify(tok, testCfg)
	require.NoError(t, err)
	assert.Equal(t, "access", claims.TokenType)
}

func TestSignRefresh_SetsTokenTypeRefresh(t *testing.T) {
	tok, err := auth.SignRefresh("user-1", testCfg)
	require.NoError(t, err)
	claims, err := auth.Verify(tok, testCfg)
	require.NoError(t, err)
	assert.Equal(t, "refresh", claims.TokenType)
}

// ── Verify ──────────────────────────────────────────────────────────────────

func TestVerify_Valid(t *testing.T) {
	tok, _ := auth.Sign("user-42", []string{"reader"}, testCfg)
	claims, err := auth.Verify(tok, testCfg)
	require.NoError(t, err)
	assert.Equal(t, "user-42", claims.Subject)
	assert.Equal(t, []string{"reader"}, claims.Roles)
}

func TestVerify_WeakSecret(t *testing.T) {
	_, err := auth.Verify("any.token.here", &auth.Config{Secret: "short"})
	assert.ErrorIs(t, err, auth.ErrWeakSecret)
}

func TestVerify_Expired(t *testing.T) {
	cfg := *testCfg
	cfg.Expiry = -time.Second // already expired
	tok, _ := auth.Sign("u", nil, &cfg)
	_, err := auth.Verify(tok, testCfg)
	assert.Error(t, err)
}

func TestVerify_WrongIssuer(t *testing.T) {
	wrongCfg := *testCfg
	wrongCfg.Issuer = "impostor"
	tok, _ := auth.Sign("u", nil, &wrongCfg)
	_, err := auth.Verify(tok, testCfg)
	assert.Error(t, err)
}

func TestVerify_WrongAudience(t *testing.T) {
	wrongCfg := *testCfg
	wrongCfg.Audience = "other-service"
	tok, _ := auth.Sign("u", nil, &wrongCfg)
	_, err := auth.Verify(tok, testCfg)
	assert.Error(t, err)
}

func TestVerify_TamperedSignature(t *testing.T) {
	tok, _ := auth.Sign("u", nil, testCfg)
	parts := strings.Split(tok, ".")
	parts[2] = "invalidsignature"
	_, err := auth.Verify(strings.Join(parts, "."), testCfg)
	assert.Error(t, err)
}

func TestVerify_AlgNoneRejected(t *testing.T) {
	// Craft a token with alg:none header manually
	// base64url({"alg":"none","typ":"JWT"}) = eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0
	noneToken := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiJ1c2VyLTEifQ."
	_, err := auth.Verify(noneToken, testCfg)
	assert.Error(t, err, "alg:none must be rejected")
}

// ── Middleware ───────────────────────────────────────────────────────────────

func newTestApp(cfg *auth.Config) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		code := fiber.StatusInternalServerError
		if e, ok := err.(*fiber.Error); ok {
			code = e.Code
		}
		return c.SendStatus(code)
	}})

	protected := app.Group("/protected", auth.Middleware(cfg))
	protected.Get("/ping", func(c *fiber.Ctx) error {
		claims := auth.GetClaims(c)
		return c.JSON(fiber.Map{"sub": claims.Subject})
	})

	admin := app.Group("/admin", auth.Middleware(cfg), auth.RequireRole("admin"))
	admin.Get("/data", func(c *fiber.Ctx) error { return c.SendStatus(200) })

	return app
}

func bearerReq(t *testing.T, app *fiber.App, method, path, token string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	return resp
}

func TestMiddleware_NoToken(t *testing.T) {
	app := newTestApp(testCfg)
	resp := bearerReq(t, app, http.MethodGet, "/protected/ping", "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMiddleware_InvalidToken(t *testing.T) {
	app := newTestApp(testCfg)
	resp := bearerReq(t, app, http.MethodGet, "/protected/ping", "not.a.jwt")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMiddleware_ValidToken(t *testing.T) {
	app := newTestApp(testCfg)
	tok, _ := auth.Sign("user-99", nil, testCfg)
	resp := bearerReq(t, app, http.MethodGet, "/protected/ping", tok)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "user-99")
}

func TestMiddleware_RefreshTokenRejected(t *testing.T) {
	// A refresh token must not pass the Bearer middleware — only access tokens are accepted.
	// This prevents a logged-out user from using a stolen refresh token to call protected routes.
	app := newTestApp(testCfg)
	tok, err := auth.SignRefresh("user-99", testCfg)
	require.NoError(t, err)
	resp := bearerReq(t, app, http.MethodGet, "/protected/ping", tok)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestRequireRole_Allowed(t *testing.T) {
	app := newTestApp(testCfg)
	tok, _ := auth.Sign("u", []string{"admin"}, testCfg)
	resp := bearerReq(t, app, http.MethodGet, "/admin/data", tok)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestRequireRole_Forbidden(t *testing.T) {
	app := newTestApp(testCfg)
	tok, _ := auth.Sign("u", []string{"reader"}, testCfg)
	resp := bearerReq(t, app, http.MethodGet, "/admin/data", tok)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestRequireRole_NoJWT(t *testing.T) {
	// RequireRole without prior Middleware — Locals has no claims
	app := fiber.New()
	app.Get("/no-auth", auth.RequireRole("admin"), func(c *fiber.Ctx) error {
		return c.SendStatus(200)
	})
	req := httptest.NewRequest(http.MethodGet, "/no-auth", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func ExampleSign() {
	cfg := &auth.Config{
		Secret:   "replace-with-a-long-random-secret-32+",
		Issuer:   "my-service",
		Audience: "my-api",
		Expiry:   time.Hour,
	}
	tok, err := auth.Sign("user-1", []string{"admin"}, cfg)
	if err != nil {
		panic(err)
	}
	_ = tok // use token in Authorization header
}

func ExampleVerify() {
	cfg := &auth.Config{
		Secret:   "replace-with-a-long-random-secret-32+",
		Issuer:   "my-service",
		Audience: "my-api",
	}
	tok := "eyJ..." // token received from client
	claims, err := auth.Verify(tok, cfg)
	if err != nil {
		// reject request
		return
	}
	_ = claims.Subject // authenticated user ID
}
