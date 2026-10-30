package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

type notifierStubUC struct {
	WorkspaceUseCase
	push func(ctx context.Context, workspaceID string, actorID uuid.UUID, changes []entity.Change) ([]entity.PushResult, int64, error)
}

func (s *notifierStubUC) PushChanges(ctx context.Context, workspaceID string, actorID uuid.UUID, changes []entity.Change) ([]entity.PushResult, int64, error) {
	return s.push(ctx, workspaceID, actorID, changes)
}

type changeNotifierSpy struct {
	mu    sync.Mutex
	calls []notifierCall
	err   error
}

type notifierCall struct {
	workspaceID string
	seq         int64
	ctxErr      error
}

func (s *changeNotifierSpy) Notify(ctx context.Context, workspaceID string, seq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, notifierCall{workspaceID: workspaceID, seq: seq, ctxErr: ctx.Err()})
	return s.err
}

func (s *changeNotifierSpy) recorded() []notifierCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]notifierCall(nil), s.calls...)
}

func TestWithChangeNotifierPublishesAfterAppliedPush(t *testing.T) {
	applied := []entity.PushResult{{OpID: "op-1", Applied: true}}
	inner := &notifierStubUC{push: func(context.Context, string, uuid.UUID, []entity.Change) ([]entity.PushResult, int64, error) {
		return applied, 12, nil
	}}
	spy := &changeNotifierSpy{}

	uc := WithChangeNotifier(inner, spy)
	results, seq, err := uc.PushChanges(context.Background(), "ws_1", uuid.New(), []entity.Change{{OpID: "op-1"}})
	require.NoError(t, err)
	require.Equal(t, applied, results)
	require.EqualValues(t, 12, seq)

	calls := spy.recorded()
	require.Len(t, calls, 1)
	require.Equal(t, "ws_1", calls[0].workspaceID)
	require.EqualValues(t, 12, calls[0].seq)
}

func TestWithChangeNotifierSkipsConflictOnlyPush(t *testing.T) {
	conflict := []entity.PushResult{{OpID: "op-1", Conflict: true, Server: &entity.ServerState{Rev: 3}}}
	inner := &notifierStubUC{push: func(context.Context, string, uuid.UUID, []entity.Change) ([]entity.PushResult, int64, error) {
		return conflict, 12, nil
	}}
	spy := &changeNotifierSpy{}

	_, _, err := WithChangeNotifier(inner, spy).PushChanges(context.Background(), "ws_1", uuid.New(), nil)
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
}

func TestWithChangeNotifierSkipsFailedPush(t *testing.T) {
	inner := &notifierStubUC{push: func(context.Context, string, uuid.UUID, []entity.Change) ([]entity.PushResult, int64, error) {
		return nil, 0, ErrForbidden
	}}
	spy := &changeNotifierSpy{}

	_, _, err := WithChangeNotifier(inner, spy).PushChanges(context.Background(), "ws_1", uuid.New(), nil)
	require.ErrorIs(t, err, ErrForbidden)
	require.Empty(t, spy.recorded())
}

func TestWithChangeNotifierNeverFailsThePush(t *testing.T) {
	inner := &notifierStubUC{push: func(context.Context, string, uuid.UUID, []entity.Change) ([]entity.PushResult, int64, error) {
		return []entity.PushResult{{Applied: true}}, 5, nil
	}}
	spy := &changeNotifierSpy{err: errors.New("pg_notify unavailable")}

	_, seq, err := WithChangeNotifier(inner, spy).PushChanges(context.Background(), "ws_1", uuid.New(), nil)
	require.NoError(t, err)
	require.EqualValues(t, 5, seq)
	require.Len(t, spy.recorded(), 1)
}

func TestWithChangeNotifierIgnoresCanceledRequestContext(t *testing.T) {
	inner := &notifierStubUC{push: func(context.Context, string, uuid.UUID, []entity.Change) ([]entity.PushResult, int64, error) {
		return []entity.PushResult{{Applied: true}}, 9, nil
	}}
	spy := &changeNotifierSpy{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := WithChangeNotifier(inner, spy).PushChanges(ctx, "ws_1", uuid.New(), nil)
	require.NoError(t, err)

	calls := spy.recorded()
	require.Len(t, calls, 1)
	require.NoError(t, calls[0].ctxErr, "notification must not inherit the canceled request context")
}

func TestWithChangeNotifierNilNotifierReturnsInner(t *testing.T) {
	inner := &notifierStubUC{}
	require.Same(t, inner, WithChangeNotifier(inner, nil))
}
