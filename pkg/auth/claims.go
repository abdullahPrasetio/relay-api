package auth

import "github.com/golang-jwt/jwt/v5"

// Claims is the JWT payload propagated through every authenticated request.
// Subject holds the user-ID; Roles carries optional RBAC role strings.
// TokenType distinguishes access tokens ("access") from refresh tokens ("refresh").
// Middleware rejects any token whose TokenType is not "access".
type Claims struct {
	jwt.RegisteredClaims
	Roles     []string `json:"roles,omitempty"`
	TokenType string   `json:"token_type,omitempty"` // "access" | "refresh"
}
