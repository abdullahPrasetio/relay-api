// Command prune removes old rows from the entity_ops idempotency log.
//
// Usage:
//
//	go run ./cmd/prune                        # delete entity_ops older than 90 days
//	go run ./cmd/prune -ops-days 30           # keep 30 days instead
//	go run ./cmd/prune -ops-days 90 -dry-run  # report candidates, delete nothing
//
// entity_changes is NOT pruned: clients pull deltas with a monotonic seq
// cursor and may resume from a very old cursor, so deleting pull-log rows
// would silently drop changes. Only entity_ops (the push idempotency log) has
// a bounded retention window.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/abdullahPrasetio/relay-api/config"
	dbrepo "github.com/abdullahPrasetio/relay-api/internal/repository/db"
	"github.com/abdullahPrasetio/relay-api/pkg/database"
)

// opsStore is the subset of dbrepo.MaintenanceRepository this command needs.
// Defining it here keeps the prune logic unit-testable without a database.
type opsStore interface {
	CountOpsBefore(ctx context.Context, before time.Time) (int64, error)
	PruneOps(ctx context.Context, before time.Time) (int64, error)
}

func main() {
	opsDays := flag.Int("ops-days", 90, "retain entity_ops rows created within this many days (0 prunes everything older than now)")
	dryRun := flag.Bool("dry-run", false, "report how many rows would be removed without deleting anything")
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprint(out, `Usage: prune [flags]

Deletes entity_ops rows older than -ops-days. entity_ops is the push
idempotency log: a retried opId older than the cutoff is applied again, so the
window must be longer than any realistic client retry.

entity_changes is NOT pruned. Clients pull deltas by seq cursor and may resume
from an old cursor; only the entity_ops idempotency log is pruned.

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if *opsDays < 0 {
		log.Fatal().Int("ops_days", *opsDays).Msg("-ops-days must be >= 0")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	gdb, err := database.NewConnection(&cfg.DB)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get underlying sql.DB")
	}
	defer func() { _ = sqlDB.Close() }()

	cutoff := cutoffFor(*opsDays, time.Now())
	repo := dbrepo.NewMaintenanceRepository(gdb)
	if err := prune(context.Background(), repo, cutoff, *dryRun, os.Stdout); err != nil {
		log.Fatal().Err(err).Msg("prune failed")
	}
}

// cutoffFor returns the UTC instant before which entity_ops rows are pruned.
func cutoffFor(days int, now time.Time) time.Time {
	return now.UTC().AddDate(0, 0, -days)
}

// prune reports the candidate or removed row count. Dry-run only reads; the
// delete path is a single statement in PruneOps.
func prune(ctx context.Context, store opsStore, cutoff time.Time, dryRun bool, out io.Writer) error {
	stamp := cutoff.Format(time.RFC3339)
	if dryRun {
		candidates, err := store.CountOpsBefore(ctx, cutoff)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "dry-run: %d entity_ops rows older than %s would be removed (nothing deleted)\n", candidates, stamp)
	} else {
		removed, err := store.PruneOps(ctx, cutoff)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "removed %d entity_ops rows older than %s\n", removed, stamp)
	}
	fmt.Fprintln(out, "note: entity_changes is NOT pruned (clients pull by cursor); only the entity_ops idempotency log is pruned")
	return nil
}
