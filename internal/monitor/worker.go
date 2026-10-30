package monitor

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

// ExecutorRunner is the worker's view of the executor.
type ExecutorRunner interface {
	Execute(ctx context.Context, monitor *entity.Monitor) (*entity.MonitorRun, error)
}

// WorkerOptions tunes the claim loop.
type WorkerOptions struct {
	PollInterval time.Duration
	BatchSize    int
	Now          func() time.Time
}

// Worker claims due monitors and executes them. Claiming advances next_run_at
// before execution, so two workers never run the same monitor slot.
type Worker struct {
	repo  domainrepo.MonitorRepository
	exec  ExecutorRunner
	poll  time.Duration
	batch int
	now   func() time.Time
}

// NewWorker builds a worker with safe defaults.
func NewWorker(repo domainrepo.MonitorRepository, exec ExecutorRunner, opts WorkerOptions) *Worker {
	if opts.PollInterval <= 0 {
		opts.PollInterval = 5 * time.Second
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 10
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Worker{repo: repo, exec: exec, poll: opts.PollInterval, batch: opts.BatchSize, now: opts.Now}
}

// Run polls until ctx is cancelled. A claim/execution error is logged and
// retried on the next tick; one bad monitor never stops the fleet.
func (w *Worker) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		if n, err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("monitor worker iteration failed")
		} else if n > 0 {
			log.Info().Int("runs", n).Msg("monitor worker executed due monitors")
		}

		timer.Reset(w.poll)
	}
}

// RunOnce claims one batch and executes every claimed monitor. It returns the
// number of executions attempted (including failed ones that were recorded).
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	claimed, err := w.repo.ClaimDue(ctx, w.now().UTC(), w.batch, NextRun)
	if err != nil {
		return 0, err
	}
	executed := 0
	for i := range claimed {
		if ctx.Err() != nil {
			break
		}
		monitor := &claimed[i]
		run, err := w.exec.Execute(ctx, monitor)
		if err != nil {
			log.Error().Err(err).
				Str("monitor_id", monitor.ID.String()).
				Msg("monitor execution failed before persistence")
			continue
		}
		executed++
		log.Info().
			Str("monitor_id", monitor.ID.String()).
			Str("run_id", run.ID.String()).
			Bool("ok", run.OK).
			Int("status", run.StatusCode).
			Int("duration_ms", run.DurationMS).
			Msg("monitor run completed")
	}
	return executed, nil
}
