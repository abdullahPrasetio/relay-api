package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeOpsStore struct {
	count      int64
	removed    int64
	countCalls int
	pruneCalls int
	err        error
}

func (f *fakeOpsStore) CountOpsBefore(_ context.Context, _ time.Time) (int64, error) {
	f.countCalls++
	return f.count, f.err
}

func (f *fakeOpsStore) PruneOps(_ context.Context, _ time.Time) (int64, error) {
	f.pruneCalls++
	return f.removed, f.err
}

func TestPruneDryRunDeletesNothing(t *testing.T) {
	store := &fakeOpsStore{count: 7}
	var out bytes.Buffer

	cutoff := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := prune(context.Background(), store, cutoff, true, &out); err != nil {
		t.Fatalf("prune dry-run: %v", err)
	}
	if store.pruneCalls != 0 {
		t.Fatalf("dry-run called PruneOps %d times, want 0", store.pruneCalls)
	}
	if store.countCalls != 1 {
		t.Fatalf("dry-run count calls = %d, want 1", store.countCalls)
	}
	got := out.String()
	if !strings.Contains(got, "7 entity_ops rows") {
		t.Fatalf("dry-run output %q does not report the candidate count", got)
	}
	if !strings.Contains(got, "2026-01-02T03:04:05Z") {
		t.Fatalf("dry-run output %q does not report the UTC cutoff", got)
	}
	if !strings.Contains(got, "entity_changes is NOT pruned") {
		t.Fatalf("dry-run output %q does not document the entity_changes exception", got)
	}
}

func TestPruneReportsRemoved(t *testing.T) {
	store := &fakeOpsStore{removed: 3}
	var out bytes.Buffer

	if err := prune(context.Background(), store, time.Now().UTC(), false, &out); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if store.countCalls != 0 || store.pruneCalls != 1 {
		t.Fatalf("calls = count %d / prune %d, want 0 / 1", store.countCalls, store.pruneCalls)
	}
	if !strings.Contains(out.String(), "removed 3 entity_ops rows") {
		t.Fatalf("output %q does not report the removed count", out.String())
	}
}

func TestPrunePropagatesError(t *testing.T) {
	store := &fakeOpsStore{err: errors.New("boom")}
	if err := prune(context.Background(), store, time.Now().UTC(), false, &bytes.Buffer{}); err == nil {
		t.Fatal("expected the store error to propagate")
	}
}

func TestCutoffForIsUTC(t *testing.T) {
	loc := time.FixedZone("UTC+7", 7*60*60)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, loc)

	got := cutoffFor(90, now)
	if got.Location() != time.UTC {
		t.Fatalf("cutoff location = %v, want UTC", got.Location())
	}
	want := time.Date(2025, 12, 10, 5, 0, 0, 0, time.UTC) // 12:00 UTC+7 is 05:00 UTC
	if !got.Equal(want) {
		t.Fatalf("cutoffFor(90) = %s, want %s", got, want)
	}

	if zero := cutoffFor(0, now); !zero.Equal(now.UTC()) {
		t.Fatalf("cutoffFor(0) = %s, want %s", zero, now.UTC())
	}
}
