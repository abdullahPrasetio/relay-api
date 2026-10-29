package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Config holds the parameters needed to sign and verify JWTs.
// Secret must come from ENV and be ≥32 bytes; shorter values are rejected.
type Config struct {
	Secret   string        // HS256 signing key — load from JWT_SECRET
	Issuer   string        // iss claim value
	Audience string        // aud claim value
	Expiry   time.Duration // token lifetime; 0 defaults to 24h
}

// ErrWeakSecret is returned when the signing secret is shorter than 32 bytes.
var ErrWeakSecret = errors.New("jwt secret must be at least 32 bytes")

// Sign creates a new HS256-signed access token for the given subject and roles.
// The returned token carries TokenType "access" and a random JTI (for blacklisting).
func Sign(subject string, roles []string, cfg *Config) (string, error) {
	return signWithType(subject, roles, "access", cfg)
}

// SignRefresh creates a refresh token for subject using cfg.
// Refresh tokens are explicitly typed "refresh" and are rejected by Middleware —
// they must only be presented to the /refresh endpoint, which verifies them directly.
func SignRefresh(subject string, cfg *Config) (string, error) {
	return signWithType(subject, nil, "refresh", cfg)
}

// signWithType is the internal signing helper shared by Sign and SignRefresh.
func signWithType(subject string, roles []string, tokenType string, cfg *Config) (string, error) {
	if len(cfg.Secret) < 32 {
		return "", ErrWeakSecret
	}
	expiry := cfg.Expiry
	if expiry == 0 {
		expiry = 24 * time.Hour
	}
	now := time.Now()
	c := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        newJTI(),
			Subject:   subject,
			Issuer:    cfg.Issuer,
			Audience:  jwt.ClaimStrings{cfg.Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
		},
		Roles:     roles,
		TokenType: tokenType,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	signed, err := token.SignedString([]byte(cfg.Secret))
	if err != nil {
		return "", fmt.Errorf("signing jwt: %w", err)
	}
	return signed, nil
}

// newJTI generates a 128-bit random hex string suitable for use as a JWT ID.
// Used for token blacklisting (logout, rotation).
func newJTI() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Verify parses and validates tokenStr.
//
// Hardening applied:
//   - algorithm pinned to HS256 — tokens with alg:none or any other algo are rejected
//   - iss, aud, exp, iat claims are all validated
//   - parsing is done via jwt.NewParser so defaults cannot be loosened accidentally
func Verify(tokenStr string, cfg *Config) (*Claims, error) {
	if len(cfg.Secret) < 32 {
		return nil, ErrWeakSecret
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), // reject alg:none
		jwt.WithIssuer(cfg.Issuer),
		jwt.WithAudience(cfg.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	token, err := parser.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(cfg.Secret), nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}
	c, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token claims")
	}
	return c, nil
}
