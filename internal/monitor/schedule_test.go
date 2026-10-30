package monitor

import (
	"testing"
	"time"
)

func TestParseCron(t *testing.T) {
	valid := []string{"*/5 * * * *", "0 9 * * 1-5", "@daily", "@hourly"}
	for _, spec := range valid {
		if _, err := ParseCron(spec); err != nil {
			t.Fatalf("ParseCron(%q) returned error: %v", spec, err)
		}
	}

	invalid := []string{"", "   ", "not a cron", "*/0 * * * *", "60 * * * *", "* * *"}
	for _, spec := range invalid {
		if _, err := ParseCron(spec); err == nil {
			t.Fatalf("ParseCron(%q) unexpectedly succeeded", spec)
		}
	}
}

func TestNextRunMath(t *testing.T) {
	from := time.Date(2026, 10, 30, 12, 3, 17, 0, time.FixedZone("WIB", 7*3600))

	next, err := NextRun("*/5 * * * *", from)
	if err != nil {
		t.Fatalf("NextRun returned error: %v", err)
	}
	want := time.Date(2026, 10, 30, 5, 5, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("NextRun = %s, want %s (UTC, strictly after from)", next, want)
	}
	if next.Location() != time.UTC {
		t.Fatalf("NextRun location = %s, want UTC", next.Location())
	}

	daily, err := NextRun("@daily", from)
	if err != nil {
		t.Fatalf("NextRun daily returned error: %v", err)
	}
	wantDaily := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	if !daily.Equal(wantDaily) {
		t.Fatalf("NextRun daily = %s, want %s", daily, wantDaily)
	}

	// A schedule exactly on the boundary advances to the next slot, never now.
	exact := time.Date(2026, 10, 30, 5, 5, 0, 0, time.UTC)
	strict, err := NextRun("*/5 * * * *", exact)
	if err != nil {
		t.Fatalf("NextRun exact returned error: %v", err)
	}
	if !strict.Equal(exact.Add(5 * time.Minute)) {
		t.Fatalf("NextRun exact = %s, want %s", strict, exact.Add(5*time.Minute))
	}
}

func TestNextRunRejectsBadSpec(t *testing.T) {
	if _, err := NextRun("bogus", time.Now()); err == nil {
		t.Fatal("NextRun accepted an invalid expression")
	}
}
