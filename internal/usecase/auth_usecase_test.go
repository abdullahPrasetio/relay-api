package usecase

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakeUserRepo struct {
	mu     sync.Mutex
	byID   map[uuid.UUID]*entity.User
	byMail map[string]*entity.User
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{byID: map[uuid.UUID]*entity.User{}, byMail: map[string]*entity.User{}}
}

func (r *fakeUserRepo) FindByID(_ context.Context, id uuid.UUID) (*entity.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.byID[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return u, nil
}

func (r *fakeUserRepo) FindByEmail(_ context.Context, email string) (*entity.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.byMail[strings.ToLower(email)]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return u, nil
}

func (r *fakeUserRepo) FindAll(context.Context) ([]*entity.User, error) { return nil, nil }

func (r *fakeUserRepo) Create(_ context.Context, user *entity.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if user.ID == uuid.Nil {
		user.ID = uuid.New()
	}
	r.byID[user.ID] = user
	r.byMail[strings.ToLower(user.Email)] = user
	return nil
}

func (r *fakeUserRepo) Update(context.Context, *entity.User) error { return nil }
func (r *fakeUserRepo) Delete(context.Context, uuid.UUID) error    { return nil }
func (r *fakeUserRepo) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	_, err := r.FindByEmail(ctx, email)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

type fakeSessionRepo struct {
	mu   sync.Mutex
	byID map[uuid.UUID]*entity.Session
}

func newFakeSessionRepo() *fakeSessionRepo {
	return &fakeSessionRepo{byID: map[uuid.UUID]*entity.Session{}}
}

func (r *fakeSessionRepo) Create(_ context.Context, s *entity.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	for _, existing := range r.byID {
		if existing.RefreshHash == s.RefreshHash {
			return errors.New("duplicate refresh hash")
		}
	}
	r.byID[s.ID] = s
	return nil
}

func (r *fakeSessionRepo) FindByRefreshHash(_ context.Context, hash string) (*entity.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.byID {
		if s.RefreshHash == hash {
			return s, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *fakeSessionRepo) FindByPrevHash(_ context.Context, hash string) (*entity.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.byID {
		if s.PrevHash != nil && *s.PrevHash == hash {
			return s, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *fakeSessionRepo) FindByID(_ context.Context, id uuid.UUID) (*entity.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.byID[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return s, nil
}

func (r *fakeSessionRepo) Rotate(_ context.Context, id uuid.UUID, oldHash, newHash string,
	replacementCT []byte, prevExpiresAt, expiresAt, usedAt time.Time, deviceLabel string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.byID[id]
	if !ok || s.RefreshHash != oldHash || s.RevokedAt != nil {
		return false, nil
	}
	s.RefreshHash = newHash
	s.PrevHash = &oldHash
	s.PrevExpiresAt = &prevExpiresAt
	s.ReplacementCT = append([]byte(nil), replacementCT...)
	s.ExpiresAt = expiresAt
	s.LastUsedAt = &usedAt
	if deviceLabel != "" {
		s.DeviceLabel = deviceLabel
	}
	return true, nil
}

func (r *fakeSessionRepo) RevokeByID(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.byID[id]
	if ok && s.RevokedAt == nil {
		now := time.Now().UTC()
		s.RevokedAt = &now
	}
	return nil
}

func (r *fakeSessionRepo) RevokeByUser(_ context.Context, userID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	for _, s := range r.byID {
		if s.UserID == userID && s.RevokedAt == nil {
			s.RevokedAt = &now
		}
	}
	return nil
}

func (r *fakeSessionRepo) ListByUser(_ context.Context, userID uuid.UUID, now time.Time) ([]*entity.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*entity.Session
	for _, s := range r.byID {
		if s.UserID == userID && s.Active(now) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *fakeSessionRepo) Touch(_ context.Context, id uuid.UUID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.byID[id]; ok {
		s.LastUsedAt = &at
	}
	return nil
}

type fakeUserUC struct {
	repo *fakeUserRepo
}

func (f *fakeUserUC) GetUser(context.Context, string) (*entity.User, error) { return nil, nil }
func (f *fakeUserUC) ListUsers(context.Context) ([]*entity.User, error)     { return nil, nil }
func (f *fakeUserUC) UpdateUser(context.Context, string, *UpdateUserRequest) (*entity.User, error) {
	return nil, nil
}
func (f *fakeUserUC) DeleteUser(context.Context, string) error { return nil }
func (f *fakeUserUC) CreateUser(ctx context.Context, req *CreateUserRequest) (*entity.User, error) {
	exists, err := f.repo.ExistsByEmail(ctx, req.Email)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrEmailConflict
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.MinCost)
	if err != nil {
		return nil, err
	}
	user := &entity.User{Name: req.Name, Email: req.Email, Password: string(hash)}
	user.ID = uuid.New()
	if err := f.repo.Create(ctx, user); err != nil {
		return nil, err
	}
	return user, nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type testClock struct{ t time.Time }

func (c *testClock) Now() time.Time          { return c.t }
func (c *testClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newAuthHarness(t *testing.T) (*authUseCase, *fakeUserRepo, *fakeSessionRepo, *testClock, *entity.User) {
	t.Helper()

	users := newFakeUserRepo()
	sessions := newFakeSessionRepo()
	uc := NewAuthUseCase(
		users,
		&fakeUserUC{repo: users},
		sessions,
		&auth.Config{
			Secret:   strings.Repeat("k", 32),
			Issuer:   "relay-test",
			Audience: "relay-api",
			Expiry:   15 * time.Minute,
		},
		AuthOptions{AccessTTL: 15 * time.Minute, RefreshTTL: 30 * 24 * time.Hour, GracePeriod: 120 * time.Second},
	).(*authUseCase)

	clock := &testClock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	uc.now = clock.Now

	hash, err := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.MinCost)
	require.NoError(t, err)
	user := &entity.User{ID: uuid.New(), Name: "Ada", Email: "ada@example.com", Password: string(hash)}
	require.NoError(t, users.Create(context.Background(), user))

	return uc, users, sessions, clock, user
}

func login(t *testing.T, uc *authUseCase, device string) *AuthTokens {
	t.Helper()
	tokens, err := uc.Login(context.Background(), &LoginRequest{
		Email: "ada@example.com", Password: "password123", DeviceLabel: device,
	})
	require.NoError(t, err)
	require.NotEmpty(t, tokens.AccessToken)
	require.NotEmpty(t, tokens.RefreshToken)
	return tokens
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestLoginIssuesTokensAndStoresOnlyHash(t *testing.T) {
	uc, _, sessions, _, user := newAuthHarness(t)

	tokens := login(t, uc, "MacBook")

	claims, err := auth.Verify(tokens.AccessToken, uc.jwtCfg)
	require.NoError(t, err)
	assert.Equal(t, user.ID.String(), claims.Subject)
	assert.Equal(t, "access", claims.TokenType)

	require.Len(t, sessions.byID, 1)
	for _, s := range sessions.byID {
		assert.NotEqual(t, tokens.RefreshToken, s.RefreshHash, "refresh token must never be stored in plaintext")
		assert.Equal(t, hashToken(tokens.RefreshToken), s.RefreshHash)
		assert.Equal(t, "MacBook", s.DeviceLabel)
		assert.Nil(t, s.RevokedAt)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	uc, _, sessions, _, _ := newAuthHarness(t)

	cases := []struct {
		name string
		req  *LoginRequest
	}{
		{"wrong password", &LoginRequest{Email: "ada@example.com", Password: "nope"}},
		{"unknown email", &LoginRequest{Email: "ghost@example.com", Password: "password123"}},
		{"empty password", &LoginRequest{Email: "ada@example.com"}},
		{"nil request", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := uc.Login(context.Background(), tc.req)
			assert.ErrorIs(t, err, ErrInvalidCredentials)
		})
	}
	assert.Empty(t, sessions.byID)
}

func TestRefreshRotationAndGraceReplay(t *testing.T) {
	uc, _, _, clock, _ := newAuthHarness(t)
	first := login(t, uc, "device")

	clock.Advance(1 * time.Second)
	rotated, err := uc.Refresh(context.Background(), &RefreshRequest{RefreshToken: first.RefreshToken})
	require.NoError(t, err)
	assert.NotEqual(t, first.RefreshToken, rotated.RefreshToken)
	assert.NotEmpty(t, rotated.AccessToken)

	// Replaying the old token inside the grace window must return the exact
	// same pair, not a second rotation.
	clock.Advance(30 * time.Second)
	replayed, err := uc.Refresh(context.Background(), &RefreshRequest{RefreshToken: first.RefreshToken})
	require.NoError(t, err)
	assert.Equal(t, rotated.RefreshToken, replayed.RefreshToken)
	assert.Equal(t, rotated.AccessToken, replayed.AccessToken)

	// The replacement token itself is still a valid current token.
	clock.Advance(1 * time.Second)
	next, err := uc.Refresh(context.Background(), &RefreshRequest{RefreshToken: rotated.RefreshToken})
	require.NoError(t, err)
	assert.NotEqual(t, rotated.RefreshToken, next.RefreshToken)
}

func TestRefreshReuseAfterGraceRevokesSession(t *testing.T) {
	uc, _, sessions, clock, _ := newAuthHarness(t)
	first := login(t, uc, "device")

	clock.Advance(1 * time.Second)
	rotated, err := uc.Refresh(context.Background(), &RefreshRequest{RefreshToken: first.RefreshToken})
	require.NoError(t, err)

	// Outside the 120s grace window the old token is reuse: 401 + whole session
	// revoked, including the token that was current until now.
	clock.Advance(121 * time.Second)
	_, err = uc.Refresh(context.Background(), &RefreshRequest{RefreshToken: first.RefreshToken})
	assert.ErrorIs(t, err, ErrInvalidRefresh)

	require.Len(t, sessions.byID, 1)
	for _, s := range sessions.byID {
		require.NotNil(t, s.RevokedAt)
	}

	_, err = uc.Refresh(context.Background(), &RefreshRequest{RefreshToken: rotated.RefreshToken})
	assert.ErrorIs(t, err, ErrInvalidRefresh)
}

func TestRefreshUnknownAndEmptyTokens(t *testing.T) {
	uc, _, sessions, _, _ := newAuthHarness(t)

	for _, token := range []string{"", "not-a-real-token"} {
		_, err := uc.Refresh(context.Background(), &RefreshRequest{RefreshToken: token})
		assert.ErrorIs(t, err, ErrInvalidRefresh)
	}
	_, err := uc.Refresh(context.Background(), nil)
	assert.ErrorIs(t, err, ErrInvalidRefresh)
	assert.Empty(t, sessions.byID)
}

func TestRefreshExpiredSession(t *testing.T) {
	uc, _, _, clock, _ := newAuthHarness(t)
	tokens := login(t, uc, "device")

	clock.Advance(30*24*time.Hour + time.Minute)

	_, err := uc.Refresh(context.Background(), &RefreshRequest{RefreshToken: tokens.RefreshToken})
	assert.ErrorIs(t, err, ErrInvalidRefresh)
}

func TestLogoutRevokesRefreshToken(t *testing.T) {
	uc, _, sessions, _, _ := newAuthHarness(t)
	tokens := login(t, uc, "device")

	require.NoError(t, uc.Logout(context.Background(), tokens.RefreshToken))
	for _, s := range sessions.byID {
		assert.NotNil(t, s.RevokedAt)
	}

	_, err := uc.Refresh(context.Background(), &RefreshRequest{RefreshToken: tokens.RefreshToken})
	assert.ErrorIs(t, err, ErrInvalidRefresh)

	// Logout of an unknown/empty token is idempotent.
	assert.NoError(t, uc.Logout(context.Background(), "unknown"))
	assert.NoError(t, uc.Logout(context.Background(), ""))
}

func TestListAndRevokeDevices(t *testing.T) {
	uc, _, _, _, user := newAuthHarness(t)
	phone := login(t, uc, "phone")
	laptop := login(t, uc, "laptop")

	devices, err := uc.ListDevices(context.Background(), user.ID.String())
	require.NoError(t, err)
	require.Len(t, devices, 2)
	labels := []string{devices[0].DeviceLabel, devices[1].DeviceLabel}
	assert.ElementsMatch(t, []string{"phone", "laptop"}, labels)

	var laptopID string
	for _, d := range devices {
		if d.DeviceLabel == "laptop" {
			laptopID = d.ID
		}
	}
	require.NotEmpty(t, laptopID)

	require.NoError(t, uc.RevokeDevice(context.Background(), user.ID.String(), laptopID))
	devices, err = uc.ListDevices(context.Background(), user.ID.String())
	require.NoError(t, err)
	require.Len(t, devices, 1)
	assert.Equal(t, "phone", devices[0].DeviceLabel)

	_, err = uc.Refresh(context.Background(), &RefreshRequest{RefreshToken: laptop.RefreshToken})
	assert.ErrorIs(t, err, ErrInvalidRefresh)

	// A device belonging to somebody else is reported as not found.
	other := &entity.User{ID: uuid.New(), Name: "Bob", Email: "bob@example.com"}
	require.NoError(t, uc.users.Create(context.Background(), other))
	first, err := uc.ListDevices(context.Background(), user.ID.String())
	require.NoError(t, err)
	err = uc.RevokeDevice(context.Background(), other.ID.String(), first[0].ID)
	assert.ErrorIs(t, err, ErrNotFound)

	// Phone session still works after the unrelated revoke attempt.
	require.NoError(t, uc.Logout(context.Background(), phone.RefreshToken))
}

func TestRegisterValidationAndConflict(t *testing.T) {
	uc, _, _, _, _ := newAuthHarness(t)

	cases := []struct {
		name string
		req  *RegisterRequest
		want error
	}{
		{"nil", nil, ErrInvalidInput},
		{"missing name", &RegisterRequest{Email: "x@example.com", Password: "password123"}, ErrInvalidInput},
		{"missing email", &RegisterRequest{Name: "X", Password: "password123"}, ErrInvalidInput},
		{"short password", &RegisterRequest{Name: "X", Email: "x@example.com", Password: "short"}, ErrInvalidInput},
		{"duplicate email", &RegisterRequest{Name: "Ada", Email: "ada@example.com", Password: "password123"}, ErrEmailConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := uc.Register(context.Background(), tc.req)
			assert.ErrorIs(t, err, tc.want)
		})
	}

	user, err := uc.Register(context.Background(), &RegisterRequest{
		Name: "Grace", Email: "grace@example.com", Password: "password123",
	})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, user.ID)
	assert.NotEqual(t, "password123", user.Password, "password must be bcrypt hashed")
}
