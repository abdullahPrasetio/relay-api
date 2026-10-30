package usecase

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

const testWorkspaceID = "ws_keys"

func testPublicKey(fill byte) string {
	return base64.StdEncoding.EncodeToString(bytesRepeat(fill, deviceKeySize))
}

func testWrappedKey(fill byte) string {
	return base64.StdEncoding.EncodeToString(bytesRepeat(fill, wrappedKeySize))
}

func bytesRepeat(fill byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = fill
	}
	return out
}

type fakeKeyAuthorizer struct {
	roles map[uuid.UUID]entity.Role
}

func (f *fakeKeyAuthorizer) GetMember(_ context.Context, workspaceID string, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	role, ok := f.roles[userID]
	if !ok || workspaceID != testWorkspaceID {
		return nil, gorm.ErrRecordNotFound
	}
	return &entity.WorkspaceMember{WorkspaceID: workspaceID, UserID: userID, Role: role}, nil
}

type fakeKeyStore struct {
	keys          map[uuid.UUID]entity.DeviceKey
	workspaceKeys []entity.WorkspaceKey
	version       int

	upsertErr error
	putArgs   *entity.WorkspaceKeyVersion
	putRows   []entity.WorkspaceKey
	rotateErr error
	listedFor uuid.UUID
}

func newFakeKeyStore() *fakeKeyStore {
	return &fakeKeyStore{keys: map[uuid.UUID]entity.DeviceKey{}}
}

func (f *fakeKeyStore) UpsertDeviceKey(_ context.Context, key *entity.DeviceKey) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.keys[key.ID] = *key
	return nil
}

func (f *fakeKeyStore) ListDeviceKeys(_ context.Context, userID uuid.UUID) ([]entity.DeviceKey, error) {
	out := []entity.DeviceKey{}
	for _, key := range f.keys {
		if key.UserID == userID {
			out = append(out, key)
		}
	}
	return out, nil
}

func (f *fakeKeyStore) RevokeDeviceKey(_ context.Context, id, userID uuid.UUID) error {
	key, ok := f.keys[id]
	if !ok || key.UserID != userID {
		return domainrepo.ErrDeviceKeyNotFound
	}
	now := time.Now().UTC()
	key.RevokedAt = &now
	f.keys[id] = key
	return nil
}

func (f *fakeKeyStore) ListWorkspaceDeviceKeys(_ context.Context, _ string) ([]entity.WorkspaceDeviceKey, error) {
	return []entity.WorkspaceDeviceKey{}, nil
}

func (f *fakeKeyStore) UpsertWorkspaceKeys(_ context.Context, version *entity.WorkspaceKeyVersion, entries []entity.WorkspaceKey) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.putArgs = version
	f.putRows = entries
	return nil
}

func (f *fakeKeyStore) ListWorkspaceKeysForUser(_ context.Context, _ string, userID uuid.UUID) ([]entity.WorkspaceKey, error) {
	f.listedFor = userID
	return f.workspaceKeys, nil
}

func (f *fakeKeyStore) RotateWorkspaceKey(_ context.Context, version *entity.WorkspaceKeyVersion) error {
	if f.rotateErr != nil {
		return f.rotateErr
	}
	f.version++
	version.Version = f.version
	version.CreatedAt = time.Now().UTC()
	return nil
}

func newKeyTestUseCase(store *fakeKeyStore) (KeyUseCase, uuid.UUID, uuid.UUID, uuid.UUID) {
	owner := uuid.New()
	admin := uuid.New()
	viewer := uuid.New()
	authz := &fakeKeyAuthorizer{roles: map[uuid.UUID]entity.Role{
		owner:  entity.RoleOwner,
		admin:  entity.RoleAdmin,
		viewer: entity.RoleViewer,
	}}
	return NewKeyUseCase(store, authz), owner, admin, viewer
}

func TestRegisterDeviceKeyValidation(t *testing.T) {
	ctx := context.Background()
	store := newFakeKeyStore()
	uc, owner, _, _ := newKeyTestUseCase(store)

	_, err := uc.RegisterDeviceKey(ctx, uuid.Nil, testPublicKey(1), "MacBook")
	require.ErrorIs(t, err, ErrValidation)

	for name, key := range map[string]string{
		"empty":    "",
		"base64":   "not-base64!!",
		"tooShort": base64.StdEncoding.EncodeToString([]byte("short")),
		"tooLong":  base64.StdEncoding.EncodeToString(bytesRepeat(2, deviceKeySize+1)),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := uc.RegisterDeviceKey(ctx, owner, key, "MacBook")
			require.ErrorIs(t, err, ErrValidation)
		})
	}

	_, err = uc.RegisterDeviceKey(ctx, owner, testPublicKey(3), strings.Repeat("x", maxDeviceKeyLabel+1))
	require.ErrorIs(t, err, ErrValidation)

	// Padded and unpadded base64 both work and are stored canonically.
	padded := testPublicKey(4)
	key, err := uc.RegisterDeviceKey(ctx, owner, "  "+padded+"  ", "  MacBook Pro  ")
	require.NoError(t, err)
	require.Equal(t, padded, key.PublicKey)
	require.Equal(t, "MacBook Pro", key.Label)
	require.Equal(t, owner, key.UserID)
	require.NotEqual(t, uuid.Nil, key.ID)
	require.Nil(t, key.RevokedAt)

	unpadded := strings.TrimRight(testPublicKey(5), "=")
	key, err = uc.RegisterDeviceKey(ctx, owner, unpadded, "")
	require.NoError(t, err)
	require.Equal(t, testPublicKey(5), key.PublicKey)
	require.Empty(t, key.Label)

	keys, err := uc.ListDeviceKeys(ctx, owner)
	require.NoError(t, err)
	require.Len(t, keys, 2)

	_, err = uc.ListDeviceKeys(ctx, uuid.Nil)
	require.ErrorIs(t, err, ErrValidation)
}

func TestRevokeDeviceKeyOwnership(t *testing.T) {
	ctx := context.Background()
	store := newFakeKeyStore()
	uc, owner, _, viewer := newKeyTestUseCase(store)

	key, err := uc.RegisterDeviceKey(ctx, owner, testPublicKey(6), "Laptop")
	require.NoError(t, err)

	require.ErrorIs(t, uc.RevokeDeviceKey(ctx, owner, uuid.Nil), ErrValidation)
	require.ErrorIs(t, uc.RevokeDeviceKey(ctx, viewer, key.ID), ErrNotFound)
	require.NoError(t, uc.RevokeDeviceKey(ctx, owner, key.ID))

	keys, err := uc.ListDeviceKeys(ctx, owner)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.NotNil(t, keys[0].RevokedAt)
}

func TestListWorkspaceDeviceKeysRequiresMembership(t *testing.T) {
	ctx := context.Background()
	store := newFakeKeyStore()
	uc, owner, _, viewer := newKeyTestUseCase(store)

	keys, err := uc.ListWorkspaceDeviceKeys(ctx, owner, testWorkspaceID)
	require.NoError(t, err)
	require.Empty(t, keys)

	_, err = uc.ListWorkspaceDeviceKeys(ctx, viewer, testWorkspaceID)
	require.NoError(t, err)

	_, err = uc.ListWorkspaceDeviceKeys(ctx, uuid.New(), testWorkspaceID)
	require.ErrorIs(t, err, ErrNotFound)

	_, err = uc.ListWorkspaceDeviceKeys(ctx, owner, "")
	require.ErrorIs(t, err, ErrValidation)
}

func TestPutWorkspaceKeysValidationAndPermissions(t *testing.T) {
	ctx := context.Background()
	store := newFakeKeyStore()
	uc, owner, admin, viewer := newKeyTestUseCase(store)
	deviceA, deviceB := uuid.New(), uuid.New()

	valid := PutWorkspaceKeysInput{
		Version: 1,
		Entries: []WrappedKeyInput{
			{DeviceKeyID: deviceA, WrappedKey: testWrappedKey(7)},
			{DeviceKeyID: deviceB, WrappedKey: testWrappedKey(8)},
		},
	}

	_, err := uc.PutWorkspaceKeys(ctx, viewer, testWorkspaceID, valid)
	require.ErrorIs(t, err, ErrForbidden)
	_, err = uc.PutWorkspaceKeys(ctx, uuid.New(), testWorkspaceID, valid)
	require.ErrorIs(t, err, ErrNotFound)

	for name, input := range map[string]PutWorkspaceKeysInput{
		"zeroVersion": valid,
		"noEntries":   {Version: 1},
		"duplicate": {Version: 1, Entries: []WrappedKeyInput{
			{DeviceKeyID: deviceA, WrappedKey: testWrappedKey(1)},
			{DeviceKeyID: deviceA, WrappedKey: testWrappedKey(2)},
		}},
		"nilDevice": {Version: 1, Entries: []WrappedKeyInput{{WrappedKey: testWrappedKey(1)}}},
		"badBase64": {Version: 1, Entries: []WrappedKeyInput{{DeviceKeyID: deviceA, WrappedKey: "%%%"}}},
		"shortWrapped": {Version: 1, Entries: []WrappedKeyInput{{
			DeviceKeyID: deviceA,
			WrappedKey:  base64.StdEncoding.EncodeToString(bytesRepeat(1, wrappedKeySize-1)),
		}}},
	} {
		if name == "zeroVersion" {
			input.Version = 0
		}
		t.Run(name, func(t *testing.T) {
			_, err := uc.PutWorkspaceKeys(ctx, admin, testWorkspaceID, input)
			require.ErrorIs(t, err, ErrValidation)
		})
	}

	version, err := uc.PutWorkspaceKeys(ctx, admin, testWorkspaceID, valid)
	require.NoError(t, err)
	require.Equal(t, 1, version.Version)
	require.Equal(t, admin, version.CreatedBy)
	require.Equal(t, testWorkspaceID, version.WorkspaceID)
	require.Equal(t, version, store.putArgs)
	require.Len(t, store.putRows, 2)
	require.Equal(t, bytesRepeat(7, wrappedKeySize), store.putRows[0].WrappedKey)
	require.Equal(t, deviceB, store.putRows[1].DeviceKeyID)

	// The owner is admin+ too.
	_, err = uc.PutWorkspaceKeys(ctx, owner, testWorkspaceID, valid)
	require.NoError(t, err)
}

func TestPutWorkspaceKeysRejectsNonMemberDeviceKey(t *testing.T) {
	ctx := context.Background()
	store := newFakeKeyStore()
	uc, _, admin, _ := newKeyTestUseCase(store)

	store.upsertErr = domainrepo.ErrDeviceKeyNotMember
	_, err := uc.PutWorkspaceKeys(ctx, admin, testWorkspaceID, PutWorkspaceKeysInput{
		Version: 1,
		Entries: []WrappedKeyInput{{DeviceKeyID: uuid.New(), WrappedKey: testWrappedKey(9)}},
	})
	require.ErrorIs(t, err, ErrValidation)
}

func TestListMyWorkspaceKeysPassesCaller(t *testing.T) {
	ctx := context.Background()
	store := newFakeKeyStore()
	uc, _, _, viewer := newKeyTestUseCase(store)

	_, err := uc.ListMyWorkspaceKeys(ctx, uuid.New(), testWorkspaceID)
	require.ErrorIs(t, err, ErrNotFound)

	keys, err := uc.ListMyWorkspaceKeys(ctx, viewer, testWorkspaceID)
	require.NoError(t, err)
	require.Empty(t, keys)
	require.Equal(t, viewer, store.listedFor)
}

func TestRotateWorkspaceKey(t *testing.T) {
	ctx := context.Background()
	store := newFakeKeyStore()
	uc, _, admin, viewer := newKeyTestUseCase(store)

	_, err := uc.RotateWorkspaceKey(ctx, viewer, testWorkspaceID)
	require.ErrorIs(t, err, ErrForbidden)
	_, err = uc.RotateWorkspaceKey(ctx, admin, "")
	require.ErrorIs(t, err, ErrValidation)

	first, err := uc.RotateWorkspaceKey(ctx, admin, testWorkspaceID)
	require.NoError(t, err)
	require.Equal(t, 1, first.Version)
	second, err := uc.RotateWorkspaceKey(ctx, admin, testWorkspaceID)
	require.NoError(t, err)
	require.Equal(t, 2, second.Version)

	store.rotateErr = domainrepo.ErrKeyVersionExists
	_, err = uc.RotateWorkspaceKey(ctx, admin, testWorkspaceID)
	require.ErrorIs(t, err, ErrConflict)
}
