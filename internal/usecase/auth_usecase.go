package usecase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

// Auth TTL defaults (plan D5/§4): short access token, long idle refresh token
// and a 120s grace window so a lost refresh response does not log the user out.
const (
	DefaultAccessTTL    = 15 * time.Minute
	DefaultRefreshTTL   = 30 * 24 * time.Hour
	DefaultRefreshGrace = 120 * time.Second
)

var authTracer = otel.Tracer("auth-usecase")

// AuthOptions controls token lifetimes. Zero values fall back to the defaults
// above; main wires them from JWT_ACCESS_TTL / JWT_REFRESH_TTL /
// JWT_REFRESH_GRACE.
type AuthOptions struct {
	AccessTTL   time.Duration
	RefreshTTL  time.Duration
	GracePeriod time.Duration
}

type RegisterRequest struct {
	Name     string `json:"name"     validate:"required,min=2,max=100"`
	Email    string `json:"email"    validate:"required,email"`
	Password string `json:"password" validate:"required,min=8,max=72"`
}

type LoginRequest struct {
	Email       string `json:"email"       validate:"required,email"`
	Password    string `json:"password"    validate:"required"`
	DeviceLabel string `json:"deviceLabel" validate:"omitempty,max=100"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refreshToken" validate:"required"`
	DeviceLabel  string `json:"deviceLabel"  validate:"omitempty,max=100"`
}

// AuthTokens is the pair returned by login/refresh. AccessToken is a short
// HS256 JWT; RefreshToken is an opaque 256-bit random string.
type AuthTokens struct {
	AccessToken  string       `json:"accessToken"`
	RefreshToken string       `json:"refreshToken"`
	User         *entity.User `json:"user,omitempty"`
}

// Device is the public projection of a session (never exposes hashes).
type Device struct {
	ID          string     `json:"id"`
	DeviceLabel string     `json:"deviceLabel"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt   time.Time  `json:"expiresAt"`
}

type AuthUseCase interface {
	Register(ctx context.Context, req *RegisterRequest) (*entity.User, error)
	Login(ctx context.Context, req *LoginRequest) (*AuthTokens, error)
	Refresh(ctx context.Context, req *RefreshRequest) (*AuthTokens, error)
	Logout(ctx context.Context, refreshToken string) error
	ListDevices(ctx context.Context, userID string) ([]Device, error)
	RevokeDevice(ctx context.Context, userID, deviceID string) error
	Me(ctx context.Context, userID string) (*entity.User, error)
}

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidRefresh     = errors.New("invalid refresh token")
	ErrInvalidInput       = errors.New("invalid input")
)

// tokenEnvelope is encrypted with AES-GCM before it touches Postgres. It holds
// the exact pair produced by the rotation so a grace-window retry returns the
// same response, not just the same refresh token.
type tokenEnvelope struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type authUseCase struct {
	users     domainrepo.UserRepository
	userUC    UserUseCase
	sessions  domainrepo.SessionRepository
	jwtCfg    *auth.Config
	opts      AuthOptions
	cipherKey []byte

	now func() time.Time
}

func NewAuthUseCase(
	users domainrepo.UserRepository,
	userUC UserUseCase,
	sessions domainrepo.SessionRepository,
	jwtCfg *auth.Config,
	opts AuthOptions,
) AuthUseCase {
	if opts.AccessTTL <= 0 {
		opts.AccessTTL = DefaultAccessTTL
	}
	if opts.RefreshTTL <= 0 {
		opts.RefreshTTL = DefaultRefreshTTL
	}
	if opts.GracePeriod <= 0 {
		opts.GracePeriod = DefaultRefreshGrace
	}
	if jwtCfg.Expiry <= 0 {
		jwtCfg.Expiry = opts.AccessTTL
	}
	return &authUseCase{
		users:     users,
		userUC:    userUC,
		sessions:  sessions,
		jwtCfg:    jwtCfg,
		opts:      opts,
		cipherKey: auth.DeriveKey(jwtCfg.Secret),
		now:       time.Now,
	}
}

func (u *authUseCase) Register(ctx context.Context, req *RegisterRequest) (*entity.User, error) {
	ctx, span := authTracer.Start(ctx, "Register")
	defer span.End()

	if req == nil || req.Name == "" || req.Email == "" || len(req.Password) < 8 {
		return nil, ErrInvalidInput
	}

	user, err := u.userUC.CreateUser(ctx, &CreateUserRequest{
		Name:     req.Name,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		if errors.Is(err, ErrEmailConflict) {
			return nil, ErrEmailConflict
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	return user, nil
}

func (u *authUseCase) Login(ctx context.Context, req *LoginRequest) (*AuthTokens, error) {
	ctx, span := authTracer.Start(ctx, "Login")
	defer span.End()

	if req == nil || req.Email == "" || req.Password == "" {
		return nil, ErrInvalidCredentials
	}

	user, err := u.users.FindByEmail(ctx, req.Email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Burn the same bcrypt cost on unknown accounts so timing does not
			// reveal whether an email is registered.
			_ = bcrypt.CompareHashAndPassword(dummyBcryptHash, []byte(req.Password))
			return nil, ErrInvalidCredentials
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("find user: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	tokens, err := u.issueSession(ctx, user, req.DeviceLabel)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	return tokens, nil
}

func (u *authUseCase) Refresh(ctx context.Context, req *RefreshRequest) (*AuthTokens, error) {
	ctx, span := authTracer.Start(ctx, "Refresh")
	defer span.End()

	if req == nil || req.RefreshToken == "" {
		return nil, ErrInvalidRefresh
	}

	now := u.now().UTC()
	hash := hashToken(req.RefreshToken)

	session, err := u.sessions.FindByRefreshHash(ctx, hash)
	switch {
	case err == nil:
		return u.rotate(ctx, session, hash, req.DeviceLabel, now)
	case errors.Is(err, gorm.ErrRecordNotFound):
		return u.grace(ctx, hash, now)
	default:
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("find session: %w", err)
	}
}

// rotate issues a fresh pair and atomically swaps the session's token hash.
// Losing the compare-and-swap means a concurrent refresh won: fall back to the
// grace path and return the pair that request produced.
func (u *authUseCase) rotate(ctx context.Context, session *entity.Session, oldHash, deviceLabel string, now time.Time) (*AuthTokens, error) {
	if !session.Active(now) {
		return nil, ErrInvalidRefresh
	}

	user, err := u.users.FindByID(ctx, session.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			_ = u.sessions.RevokeByID(ctx, session.ID)
			return nil, ErrInvalidRefresh
		}
		return nil, fmt.Errorf("find session user: %w", err)
	}

	tokens, err := u.newTokens(user)
	if err != nil {
		return nil, err
	}

	sealed, err := u.sealTokens(tokens)
	if err != nil {
		return nil, err
	}

	rotated, err := u.sessions.Rotate(
		ctx,
		session.ID,
		oldHash,
		hashToken(tokens.RefreshToken),
		sealed,
		now.Add(u.opts.GracePeriod),
		now.Add(u.opts.RefreshTTL),
		now,
		deviceLabel,
	)
	if err != nil {
		return nil, fmt.Errorf("rotate session: %w", err)
	}
	if !rotated {
		return u.grace(ctx, oldHash, now)
	}
	return tokens, nil
}

// grace returns the pair produced by the last rotation when the presented
// token is that session's previous token and the grace window is still open.
// Presenting it after the window is reuse: the whole session is revoked.
func (u *authUseCase) grace(ctx context.Context, oldHash string, now time.Time) (*AuthTokens, error) {
	session, err := u.sessions.FindByPrevHash(ctx, oldHash)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidRefresh
		}
		return nil, fmt.Errorf("find session by previous hash: %w", err)
	}
	if session.RevokedAt != nil {
		return nil, ErrInvalidRefresh
	}
	if session.PrevExpiresAt == nil || !now.Before(*session.PrevExpiresAt) {
		if err := u.sessions.RevokeByID(ctx, session.ID); err != nil {
			return nil, fmt.Errorf("revoke reused session: %w", err)
		}
		return nil, ErrInvalidRefresh
	}

	envelope, err := u.openTokens(session.ReplacementCT)
	if err != nil {
		// A corrupt/undecryptable envelope (e.g. JWT secret rotated) cannot be
		// replayed; revoke rather than leaving a half-broken session.
		_ = u.sessions.RevokeByID(ctx, session.ID)
		return nil, ErrInvalidRefresh
	}

	user, err := u.users.FindByID(ctx, session.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			_ = u.sessions.RevokeByID(ctx, session.ID)
			return nil, ErrInvalidRefresh
		}
		return nil, fmt.Errorf("find session user: %w", err)
	}

	if err := u.sessions.Touch(ctx, session.ID, now); err != nil {
		return nil, fmt.Errorf("touch session: %w", err)
	}

	return &AuthTokens{
		AccessToken:  envelope.AccessToken,
		RefreshToken: envelope.RefreshToken,
		User:         user,
	}, nil
}

func (u *authUseCase) Logout(ctx context.Context, refreshToken string) error {
	ctx, span := authTracer.Start(ctx, "Logout")
	defer span.End()

	if refreshToken == "" {
		return nil
	}
	hash := hashToken(refreshToken)

	session, err := u.sessions.FindByRefreshHash(ctx, hash)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return fmt.Errorf("find session: %w", err)
		}
		// The token may be the rotated-away one; revoke its session as well.
		session, err = u.sessions.FindByPrevHash(ctx, hash)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // idempotent logout
			}
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return fmt.Errorf("find session by previous hash: %w", err)
		}
	}

	if err := u.sessions.RevokeByID(ctx, session.ID); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func (u *authUseCase) ListDevices(ctx context.Context, userID string) ([]Device, error) {
	ctx, span := authTracer.Start(ctx, "ListDevices")
	defer span.End()
	span.SetAttributes(attribute.String("user.id", userID))

	uid, err := parseUUID(userID)
	if err != nil {
		return nil, err
	}
	sessions, err := u.sessions.ListByUser(ctx, uid, u.now().UTC())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("list devices: %w", err)
	}
	devices := make([]Device, 0, len(sessions))
	for _, s := range sessions {
		devices = append(devices, Device{
			ID:          s.ID.String(),
			DeviceLabel: s.DeviceLabel,
			CreatedAt:   s.CreatedAt,
			LastUsedAt:  s.LastUsedAt,
			ExpiresAt:   s.ExpiresAt,
		})
	}
	return devices, nil
}

func (u *authUseCase) RevokeDevice(ctx context.Context, userID, deviceID string) error {
	ctx, span := authTracer.Start(ctx, "RevokeDevice")
	defer span.End()
	span.SetAttributes(attribute.String("user.id", userID), attribute.String("device.id", deviceID))

	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	did, err := parseUUID(deviceID)
	if err != nil {
		return err
	}

	session, err := u.sessions.FindByID(ctx, did)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("find session: %w", err)
	}
	// Do not reveal that another user's device exists.
	if session.UserID != uid {
		return ErrNotFound
	}
	if err := u.sessions.RevokeByID(ctx, did); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func (u *authUseCase) Me(ctx context.Context, userID string) (*entity.User, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, err
	}
	user, err := u.users.FindByID(ctx, uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find user: %w", err)
	}
	return user, nil
}

func (u *authUseCase) issueSession(ctx context.Context, user *entity.User, deviceLabel string) (*AuthTokens, error) {
	tokens, err := u.newTokens(user)
	if err != nil {
		return nil, err
	}

	now := u.now().UTC()
	session := &entity.Session{
		UserID:      user.ID,
		DeviceLabel: deviceLabel,
		RefreshHash: hashToken(tokens.RefreshToken),
		ExpiresAt:   now.Add(u.opts.RefreshTTL),
		CreatedAt:   now,
		LastUsedAt:  &now,
	}
	if err := u.sessions.Create(ctx, session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return tokens, nil
}

func (u *authUseCase) newTokens(user *entity.User) (*AuthTokens, error) {
	access, err := auth.Sign(user.ID.String(), nil, u.jwtCfg)
	if err != nil {
		return nil, fmt.Errorf("sign access token: %w", err)
	}
	refresh, err := newOpaqueToken()
	if err != nil {
		return nil, err
	}
	return &AuthTokens{AccessToken: access, RefreshToken: refresh, User: user}, nil
}

func (u *authUseCase) sealTokens(tokens *AuthTokens) ([]byte, error) {
	// #nosec G117 -- the envelope is encrypted with AES-GCM below and never logged.
	payload, err := json.Marshal(tokenEnvelope{
		AccessToken:  tokens.AccessToken,  // #nosec G117 -- sealed immediately below
		RefreshToken: tokens.RefreshToken, // #nosec G117 -- sealed immediately below
	})
	if err != nil {
		return nil, fmt.Errorf("encode replacement tokens: %w", err)
	}
	sealed, err := auth.Encrypt(u.cipherKey, payload)
	if err != nil {
		return nil, fmt.Errorf("encrypt replacement tokens: %w", err)
	}
	return sealed, nil
}

func (u *authUseCase) openTokens(sealed []byte) (*tokenEnvelope, error) {
	if len(sealed) == 0 {
		return nil, errors.New("empty replacement envelope")
	}
	payload, err := auth.Decrypt(u.cipherKey, sealed)
	if err != nil {
		return nil, err
	}
	var envelope tokenEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("decode replacement tokens: %w", err)
	}
	if envelope.AccessToken == "" || envelope.RefreshToken == "" {
		return nil, errors.New("incomplete replacement envelope")
	}
	return &envelope, nil
}

func newOpaqueToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// dummyBcryptHash is a bcrypt hash of a random throwaway value, used only to
// keep the unknown-email login path as expensive as a real comparison.
var dummyBcryptHash = []byte("$2a$10$8XC7G1rx1tvZlfxteceTGu5jcBa//L86S8834xC9IKZtNDJMuAghC")
