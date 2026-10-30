package usecase

import (
	"context"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

// ChangeNotifier publishes a workspace's new append-log sequence so live
// clients know they should pull. realtime.Notifier implements it with
// pg_notify; tests can pass a fake.
type ChangeNotifier interface {
	Notify(ctx context.Context, workspaceID string, seq int64) error
}

// notifyingWorkspaceUseCase decorates the workspace usecase with a best-effort
// realtime signal published after a push commits.
type notifyingWorkspaceUseCase struct {
	WorkspaceUseCase
	notifier ChangeNotifier
}

// WithChangeNotifier wraps uc so that a PushChanges that applied at least one
// change publishes {workspaceId, seq} after the repository transaction commits.
// A notification failure never fails the push: the signal is a hint and
// clients catch up on their next pull.
//
// Wiring:
//
//	workspaceUC := usecase.WithChangeNotifier(
//		usecase.NewWorkspaceUseCase(workspaceRepo),
//		realtime.NewNotifier(sqlDB),
//	)
func WithChangeNotifier(uc WorkspaceUseCase, notifier ChangeNotifier) WorkspaceUseCase {
	if uc == nil || notifier == nil {
		return uc
	}
	return &notifyingWorkspaceUseCase{WorkspaceUseCase: uc, notifier: notifier}
}

func (u *notifyingWorkspaceUseCase) PushChanges(ctx context.Context, workspaceID string, actorID uuid.UUID, changes []entity.Change) ([]entity.PushResult, int64, error) {
	results, seq, err := u.WorkspaceUseCase.PushChanges(ctx, workspaceID, actorID, changes)
	if err != nil {
		return results, seq, err
	}

	applied := false
	for _, result := range results {
		if result.Applied {
			applied = true
			break
		}
	}
	// A conflict-only retry does not move the append log, so there is nothing
	// to wake clients up for.
	if !applied || seq <= 0 {
		return results, seq, nil
	}

	// The push committed; publish even if the client that sent it disconnected.
	notifyCtx := context.WithoutCancel(ctx)
	if err := u.notifier.Notify(notifyCtx, workspaceID, seq); err != nil {
		log.Warn().Err(err).
			Str("workspace", workspaceID).
			Int64("seq", seq).
			Msg("realtime change notification failed; clients will pull on next signal")
	}
	return results, seq, nil
}
