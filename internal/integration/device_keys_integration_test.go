//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/route"
	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
	dbrepo "github.com/abdullahPrasetio/relay-api/internal/repository/db"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/migrations"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

type keyHarness struct {
	db          *gorm.DB
	app         *fiber.App
	cfg         *auth.Config
	workspaceUC usecase.WorkspaceUseCase
	keys        domainrepo.DeviceKeyRepository
	workspaceID string
	owner       uuid.UUID
	admin       uuid.UUID
	editor      uuid.UUID
	viewer      uuid.UUID
	outsider    uuid.UUID
	tokens      map[uuid.UUID]string
}

func newKeyHarness(t *testing.T) *keyHarness {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	sqlDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping())

	_, err = sqlDB.Exec(`DROP SCHEMA IF EXISTS public CASCADE`)
	require.NoError(t, err)
	_, err = sqlDB.Exec(`CREATE SCHEMA public`)
	require.NoError(t, err)

	src, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	driver, err := migratepostgres.WithInstance(sqlDB, &migratepostgres.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	require.NoError(t, err)
	require.NoError(t, m.Up())

	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	t.Cleanup(func() {
		if raw, err := gdb.DB(); err == nil {
			_ = raw.Close()
		}
	})

	h := &keyHarness{
		db:       gdb,
		owner:    uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		admin:    uuid.MustParse("22222222-2222-4222-8222-222222222222"),
		editor:   uuid.MustParse("33333333-3333-4333-8333-333333333333"),
		viewer:   uuid.MustParse("44444444-4444-4444-8444-444444444444"),
		outsider: uuid.MustParse("55555555-5555-4555-8555-555555555555"),
		tokens:   map[uuid.UUID]string{},
	}
	h.workspaceUC = usecase.NewWorkspaceUseCase(dbrepo.NewWorkspaceRepository(gdb))
	h.keys = dbrepo.NewDeviceKeyRepository(gdb)

	for i, id := range []uuid.UUID{h.owner, h.admin, h.editor, h.viewer, h.outsider} {
		require.NoError(t, gdb.Exec(
			`INSERT INTO users (id, name, email, password) VALUES (?, ?, ?, ?)`,
			id, "key-user", "keys"+string(rune('a'+i))+"@example.test", "hash",
		).Error)
	}

	ws, err := h.workspaceUC.CreateWorkspace(context.Background(), h.owner, "Keys Team")
	require.NoError(t, err)
	h.workspaceID = ws.ID
	_, err = h.workspaceUC.AddMember(context.Background(), h.owner, ws.ID, h.admin, entity.RoleAdmin)
	require.NoError(t, err)
	_, err = h.workspaceUC.AddMember(context.Background(), h.owner, ws.ID, h.editor, entity.RoleEditor)
	require.NoError(t, err)
	_, err = h.workspaceUC.AddMember(context.Background(), h.owner, ws.ID, h.viewer, entity.RoleViewer)
	require.NoError(t, err)

	h.cfg = &auth.Config{
		Secret:   strings.Repeat("k", 32),
		Issuer:   "relay-test",
		Audience: "relay-api",
		Expiry:   time.Hour,
	}
	h.app = fiber.New()
	api := h.app.Group("/api/v1")
	route.RegisterKeyRoutes(api, handler.NewKeyHandler(usecase.NewKeyUseCase(h.keys, dbrepo.NewWorkspaceRepository(gdb))), h.cfg)

	return h
}

type keyAPIResponse struct {
	Status  bool            `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Code    string          `json:"code"`
}

func (h *keyHarness) token(t *testing.T, uid uuid.UUID) string {
	t.Helper()
	if token, ok := h.tokens[uid]; ok {
		return token
	}
	token, err := auth.Sign(uid.String(), nil, h.cfg)
	require.NoError(t, err)
	h.tokens[uid] = token
	return token
}

// request performs one authenticated call. user == uuid.Nil sends no
// Authorization header.
func (h *keyHarness) request(t *testing.T, user uuid.UUID, method, path string, body any) (int, keyAPIResponse) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != uuid.Nil {
		req.Header.Set("Authorization", "Bearer "+h.token(t, user))
	}

	resp, err := h.app.Test(req, -1)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var out keyAPIResponse
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

func (h *keyHarness) register(t *testing.T, user uuid.UUID, publicKey, label string) *entity.DeviceKey {
	t.Helper()
	status, resp := h.request(t, user, http.MethodPut, "/api/v1/devices/keys", fiber.Map{
		"publicKey": publicKey,
		"label":     label,
	})
	require.Equal(t, http.StatusOK, status, "register device key: %s", resp.Message)
	var key entity.DeviceKey
	require.NoError(t, json.Unmarshal(resp.Data, &key))
	return &key
}

func (h *keyHarness) myKeys(t *testing.T, user uuid.UUID) []entity.DeviceKey {
	t.Helper()
	status, resp := h.request(t, user, http.MethodGet, "/api/v1/devices/keys", nil)
	require.Equal(t, http.StatusOK, status, resp.Message)
	var keys []entity.DeviceKey
	require.NoError(t, json.Unmarshal(resp.Data, &keys))
	return keys
}

func keyPublicKey(fill byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func keyWrappedKey(fill byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 92))
}

func TestDeviceKeyRegistrationAndOwnership(t *testing.T) {
	h := newKeyHarness(t)

	// Unauthenticated registration is rejected by the auth middleware.
	status, _ := h.request(t, uuid.Nil, http.MethodPut, "/api/v1/devices/keys", fiber.Map{
		"publicKey": keyPublicKey(1),
	})
	require.Equal(t, http.StatusUnauthorized, status)

	key := h.register(t, h.owner, keyPublicKey(1), "Owner laptop")
	require.Equal(t, h.owner, key.UserID)
	require.Equal(t, keyPublicKey(1), key.PublicKey)
	require.Equal(t, "Owner laptop", key.Label)
	require.Nil(t, key.RevokedAt)
	require.NotEqual(t, uuid.Nil, key.ID)

	// Re-registering the same public key updates the label in place.
	again := h.register(t, h.owner, keyPublicKey(1), "Owner laptop (renamed)")
	require.Equal(t, key.ID, again.ID)
	require.Equal(t, "Owner laptop (renamed)", again.Label)

	// Unpadded base64 is accepted and stored canonically.
	unpadded := strings.TrimRight(keyPublicKey(2), "=")
	second := h.register(t, h.owner, unpadded, "Desktop")
	require.Equal(t, keyPublicKey(2), second.PublicKey)

	// Invalid key material is a validation error.
	for name, publicKey := range map[string]string{
		"not base64": "%%%",
		"too short":  base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")),
		"empty":      "",
	} {
		t.Run(name, func(t *testing.T) {
			status, resp := h.request(t, h.owner, http.MethodPut, "/api/v1/devices/keys", fiber.Map{"publicKey": publicKey})
			require.Equal(t, http.StatusUnprocessableEntity, status)
			require.Equal(t, "ERR_VALIDATION", resp.Code)
		})
	}

	keys := h.myKeys(t, h.owner)
	require.Len(t, keys, 2)

	// Own-keys-only: the outsider sees nothing.
	require.Empty(t, h.myKeys(t, h.outsider))

	// Another user cannot revoke someone else's key.
	status, _ = h.request(t, h.outsider, http.MethodDelete, "/api/v1/devices/keys/"+key.ID.String(), nil)
	require.Equal(t, http.StatusNotFound, status)

	// Revoking is idempotent and keeps the key visible to its owner.
	for range 2 {
		status, _ = h.request(t, h.owner, http.MethodDelete, "/api/v1/devices/keys/"+key.ID.String(), nil)
		require.Equal(t, http.StatusNoContent, status)
	}
	keys = h.myKeys(t, h.owner)
	require.Len(t, keys, 2)
	var revoked int
	for _, owned := range keys {
		if owned.ID == key.ID {
			require.NotNil(t, revokedAt(owned))
			revoked++
		}
	}
	require.Equal(t, 1, revoked)
}

func revokedAt(key entity.DeviceKey) *time.Time { return key.RevokedAt }

func TestWorkspaceDeviceKeysRequireMembership(t *testing.T) {
	h := newKeyHarness(t)

	ownerKey := h.register(t, h.owner, keyPublicKey(1), "Owner")
	h.register(t, h.admin, keyPublicKey(2), "Admin")
	editorKey := h.register(t, h.editor, keyPublicKey(3), "Editor")
	h.register(t, h.viewer, keyPublicKey(4), "Viewer")

	status, resp := h.request(t, h.editor, http.MethodGet, "/api/v1/workspaces/"+h.workspaceID+"/device-keys", nil)
	require.Equal(t, http.StatusOK, status, resp.Message)
	var keys []entity.WorkspaceDeviceKey
	require.NoError(t, json.Unmarshal(resp.Data, &keys))
	require.Len(t, keys, 4)
	for _, key := range keys {
		require.NotEqual(t, uuid.Nil, key.ID)
		require.NotEqual(t, uuid.Nil, key.UserID)
		require.NotEmpty(t, key.PublicKey)
	}

	// Non-members get 404, never 403, so workspace existence is not leaked.
	status, _ = h.request(t, h.outsider, http.MethodGet, "/api/v1/workspaces/"+h.workspaceID+"/device-keys", nil)
	require.Equal(t, http.StatusNotFound, status)

	// Revoked keys drop out of the recipient list.
	status, _ = h.request(t, h.editor, http.MethodDelete, "/api/v1/devices/keys/"+editorKey.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, status)
	status, resp = h.request(t, h.owner, http.MethodGet, "/api/v1/workspaces/"+h.workspaceID+"/device-keys", nil)
	require.Equal(t, http.StatusOK, status)
	keys = nil
	require.NoError(t, json.Unmarshal(resp.Data, &keys))
	require.Len(t, keys, 3)

	// Removing a member removes their keys from the list.
	require.NoError(t, h.workspaceUC.RemoveMember(context.Background(), h.owner, h.workspaceID, h.viewer))
	status, resp = h.request(t, h.owner, http.MethodGet, "/api/v1/workspaces/"+h.workspaceID+"/device-keys", nil)
	require.Equal(t, http.StatusOK, status)
	keys = nil
	require.NoError(t, json.Unmarshal(resp.Data, &keys))
	require.Len(t, keys, 2)
	require.Equal(t, ownerKey.ID, keys[0].ID)
}

type wrappedKeyEntry struct {
	DeviceKeyID uuid.UUID `json:"deviceKeyId"`
	WrappedKey  string    `json:"wrappedKey"`
}

type workspaceVersionDTO struct {
	WorkspaceID string    `json:"workspaceId"`
	Version     int       `json:"version"`
	CreatedBy   uuid.UUID `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
}

type myWrappedKeyDTO struct {
	DeviceKeyID uuid.UUID `json:"deviceKeyId"`
	Version     int       `json:"version"`
	WrappedKey  string    `json:"wrappedKey"`
}

func (h *keyHarness) putWorkspaceKeys(t *testing.T, user uuid.UUID, version int, entries []wrappedKeyEntry) (int, workspaceVersionDTO) {
	t.Helper()
	status, resp := h.request(t, user, http.MethodPost, "/api/v1/workspaces/"+h.workspaceID+"/keys", fiber.Map{
		"version": version,
		"entries": entries,
	})
	if status != http.StatusOK {
		return status, workspaceVersionDTO{}
	}
	var out workspaceVersionDTO
	require.NoError(t, json.Unmarshal(resp.Data, &out))
	return status, out
}

func (h *keyHarness) myWorkspaceKeys(t *testing.T, user uuid.UUID) []myWrappedKeyDTO {
	t.Helper()
	status, resp := h.request(t, user, http.MethodGet, "/api/v1/workspaces/"+h.workspaceID+"/keys", nil)
	require.Equal(t, http.StatusOK, status, resp.Message)
	var keys []myWrappedKeyDTO
	require.NoError(t, json.Unmarshal(resp.Data, &keys))
	return keys
}

func TestWorkspaceKeyUploadAndOwnReads(t *testing.T) {
	h := newKeyHarness(t)

	ownerKey := h.register(t, h.owner, keyPublicKey(1), "Owner")
	adminKey := h.register(t, h.admin, keyPublicKey(2), "Admin")
	h.register(t, h.editor, keyPublicKey(3), "Editor")
	revokedKey := h.register(t, h.admin, keyPublicKey(4), "Old admin")

	entries := []wrappedKeyEntry{
		{DeviceKeyID: ownerKey.ID, WrappedKey: keyWrappedKey(10)},
		{DeviceKeyID: adminKey.ID, WrappedKey: keyWrappedKey(11)},
	}

	// Only admin+ may upload wrapped keys; non-members are 404.
	status, _ := h.putWorkspaceKeys(t, h.viewer, 1, entries)
	require.Equal(t, http.StatusForbidden, status)
	status, _ = h.putWorkspaceKeys(t, h.editor, 1, entries)
	require.Equal(t, http.StatusForbidden, status)
	status, _ = h.putWorkspaceKeys(t, h.outsider, 1, entries)
	require.Equal(t, http.StatusNotFound, status)

	// Malformed payloads are validation errors.
	status, _ = h.putWorkspaceKeys(t, h.admin, 0, entries)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	status, _ = h.putWorkspaceKeys(t, h.admin, 1, nil)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	status, _ = h.putWorkspaceKeys(t, h.admin, 1, []wrappedKeyEntry{
		{DeviceKeyID: ownerKey.ID, WrappedKey: base64.StdEncoding.EncodeToString([]byte("short"))},
	})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	status, _ = h.putWorkspaceKeys(t, h.admin, 1, []wrappedKeyEntry{
		{DeviceKeyID: uuid.New(), WrappedKey: keyWrappedKey(12)},
	})
	require.Equal(t, http.StatusUnprocessableEntity, status)

	// A revoked device key is no longer a valid recipient.
	status, _ = h.request(t, h.admin, http.MethodDelete, "/api/v1/devices/keys/"+revokedKey.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = h.putWorkspaceKeys(t, h.admin, 1, []wrappedKeyEntry{
		{DeviceKeyID: revokedKey.ID, WrappedKey: keyWrappedKey(13)},
	})
	require.Equal(t, http.StatusUnprocessableEntity, status)

	// Admin stores version 1; the version row is created on first upload.
	status, version := h.putWorkspaceKeys(t, h.admin, 1, entries)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, 1, version.Version)
	require.Equal(t, h.admin, version.CreatedBy)
	require.Equal(t, h.workspaceID, version.WorkspaceID)

	// Reads return only the caller's wrapped keys.
	ownerRead := h.myWorkspaceKeys(t, h.owner)
	require.Len(t, ownerRead, 1)
	require.Equal(t, ownerKey.ID, ownerRead[0].DeviceKeyID)
	require.Equal(t, 1, ownerRead[0].Version)
	require.Equal(t, keyWrappedKey(10), ownerRead[0].WrappedKey)

	require.Empty(t, h.myWorkspaceKeys(t, h.editor))

	// Upserting the same version replaces the envelope and keeps the version
	// row's original creator.
	status, version = h.putWorkspaceKeys(t, h.owner, 1, []wrappedKeyEntry{
		{DeviceKeyID: ownerKey.ID, WrappedKey: keyWrappedKey(14)},
	})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, h.admin, version.CreatedBy)
	require.Equal(t, keyWrappedKey(14), h.myWorkspaceKeys(t, h.owner)[0].WrappedKey)

	// A later version can be uploaded without rotating first.
	status, version = h.putWorkspaceKeys(t, h.owner, 2, []wrappedKeyEntry{
		{DeviceKeyID: ownerKey.ID, WrappedKey: keyWrappedKey(15)},
	})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, h.owner, version.CreatedBy)
	ownerRead = h.myWorkspaceKeys(t, h.owner)
	require.Len(t, ownerRead, 2)
	require.Equal(t, 2, ownerRead[0].Version)
	require.Equal(t, 1, ownerRead[1].Version)

	// Non-members cannot read.
	status, _ = h.request(t, h.outsider, http.MethodGet, "/api/v1/workspaces/"+h.workspaceID+"/keys", nil)
	require.Equal(t, http.StatusNotFound, status)
}

func TestWorkspaceKeyRotation(t *testing.T) {
	h := newKeyHarness(t)
	ownerKey := h.register(t, h.owner, keyPublicKey(1), "Owner")
	h.register(t, h.admin, keyPublicKey(2), "Admin")

	// Rotation is admin+ only.
	status, _ := h.request(t, h.editor, http.MethodPost, "/api/v1/workspaces/"+h.workspaceID+"/keys/rotate", nil)
	require.Equal(t, http.StatusForbidden, status)
	status, _ = h.request(t, h.outsider, http.MethodPost, "/api/v1/workspaces/"+h.workspaceID+"/keys/rotate", nil)
	require.Equal(t, http.StatusNotFound, status)

	rotate := func(user uuid.UUID) (int, workspaceVersionDTO) {
		status, resp := h.request(t, user, http.MethodPost, "/api/v1/workspaces/"+h.workspaceID+"/keys/rotate", nil)
		if status != http.StatusCreated {
			return status, workspaceVersionDTO{}
		}
		var out workspaceVersionDTO
		require.NoError(t, json.Unmarshal(resp.Data, &out))
		return status, out
	}

	status, first := rotate(h.admin)
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, 1, first.Version)
	require.Equal(t, h.admin, first.CreatedBy)

	status, second := rotate(h.admin)
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, 2, second.Version)

	status, third := rotate(h.owner)
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, 3, third.Version)

	// Wrapped keys for the rotated version are stored against it.
	status, version := h.putWorkspaceKeys(t, h.admin, 3, []wrappedKeyEntry{
		{DeviceKeyID: ownerKey.ID, WrappedKey: keyWrappedKey(20)},
	})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, 3, version.Version)

	status, fourth := rotate(h.admin)
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, 4, fourth.Version)

	var versions []int
	require.NoError(t, h.db.Raw(
		`SELECT version FROM workspace_key_versions WHERE workspace_id = ? ORDER BY version`, h.workspaceID,
	).Scan(&versions).Error)
	require.Equal(t, []int{1, 2, 3, 4}, versions)

	read := h.myWorkspaceKeys(t, h.owner)
	require.Len(t, read, 1)
	require.Equal(t, 3, read[0].Version)
}

// TestRotateRejectsDuplicateVersion races rotations and asserts the unique
// version invariant: a loser reports ErrKeyVersionExists instead of
// overwriting, and no version is ever handed out twice.
func TestRotateRejectsDuplicateVersion(t *testing.T) {
	h := newKeyHarness(t)

	const workers = 6
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		versions []int
		errs     []error
	)
	start := make(chan struct{})
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			version := &entity.WorkspaceKeyVersion{WorkspaceID: h.workspaceID, CreatedBy: h.admin}
			err := h.keys.RotateWorkspaceKey(context.Background(), version)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			versions = append(versions, version.Version)
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range errs {
		require.True(t, errors.Is(err, domainrepo.ErrKeyVersionExists), "unexpected rotation error: %v", err)
	}
	require.NotEmpty(t, versions)

	seen := map[int]bool{}
	for _, v := range versions {
		require.False(t, seen[v], "version %d handed out twice", v)
		seen[v] = true
	}

	var stored int
	require.NoError(t, h.db.Raw(
		`SELECT count(*) FROM workspace_key_versions WHERE workspace_id = ?`, h.workspaceID,
	).Scan(&stored).Error)
	require.Equal(t, len(versions), stored)
}
