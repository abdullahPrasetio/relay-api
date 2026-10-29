package auth

import (
	"strings"

	"github.com/gofiber/fiber/v2"
)

const claimsKey = "jwt_claims"

// Middleware validates the Bearer token in the Authorization header.
// On success the verified *Claims are stored in Fiber Locals and are accessible
// via GetClaims(c). On failure the request is rejected with 401 Unauthorized.
func Middleware(cfg *Config) fiber.Handler {
	return func(c *fiber.Ctx) error {
		raw := c.Get("Authorization")
		if !strings.HasPrefix(raw, "Bearer ") {
			return fiber.ErrUnauthorized
		}
		claims, err := Verify(strings.TrimPrefix(raw, "Bearer "), cfg)
		if err != nil {
			return fiber.ErrUnauthorized
		}
		// Reject tokens explicitly typed as non-access (e.g. refresh tokens).
		// Tokens without token_type (issued before this field was introduced) still pass
		// to preserve backward compatibility.
		if claims.TokenType != "" && claims.TokenType != "access" {
			return fiber.ErrUnauthorized
		}
		c.Locals(claimsKey, claims)
		return c.Next()
	}
}

// RequireRole returns a middleware that allows the request to continue only
// when the JWT stored by Middleware carries at least one of the given roles.
func RequireRole(roles ...string) fiber.Handler {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}
	return func(c *fiber.Ctx) error {
		claims := GetClaims(c)
		if claims == nil {
			return fiber.ErrUnauthorized
		}
		for _, r := range claims.Roles {
			if _, ok := allowed[r]; ok {
				return c.Next()
			}
		}
		return fiber.ErrForbidden
	}
}

// GetClaims retrieves the *Claims stored by Middleware from Fiber Locals.
// Returns nil on public routes where Middleware was not applied.
func GetClaims(c *fiber.Ctx) *Claims {
	v := c.Locals(claimsKey)
	if v == nil {
		return nil
	}
	claims, _ := v.(*Claims)
	return claims
}
