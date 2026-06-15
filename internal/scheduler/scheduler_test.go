package scheduler

import (
	"context"
	"testing"
	"time"
)

func TestParseDailyAt(t *testing.T) {
	h, m, err := ParseDailyAt("09:30")
	if err != nil || h != 9 || m != 30 {
		t.Fatalf("ParseDailyAt(09:30) = %d:%d err=%v", h, m, err)
	}
	for _, bad := range []string{"", "9", "25:00", "09:60", "ab:cd", "09:30:00"} {
		if _, _, err := ParseDailyAt(bad); err == nil {
			t.Errorf("ParseDailyAt(%q) should error", bad)
		}
	}
}

func TestNextFire(t *testing.T) {
	loc := time.UTC
	// now before today's 09:00 -> today 09:00
	now := time.Date(2026, 6, 10, 8, 0, 0, 0, loc)
	got := NextFire(now, 9, 0)
	want := time.Date(2026, 6, 10, 9, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("before: NextFire = %v, want %v", got, want)
	}
	// now after today's 09:00 -> tomorrow 09:00
	now = time.Date(2026, 6, 10, 10, 0, 0, 0, loc)
	got = NextFire(now, 9, 0)
	want = time.Date(2026, 6, 11, 9, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("after: NextFire = %v, want %v", got, want)
	}
	// exactly at 09:00 -> next day (strictly after)
	now = time.Date(2026, 6, 10, 9, 0, 0, 0, loc)
	got = NextFire(now, 9, 0)
	if !got.Equal(time.Date(2026, 6, 11, 9, 0, 0, 0, loc)) {
		t.Errorf("exact: NextFire = %v, want tomorrow", got)
	}
}

func TestMissedToday(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 10, 10, 0, 0, 0, loc) // past 09:00
	// last run was yesterday -> missed
	if !MissedToday(now, time.Date(2026, 6, 9, 9, 0, 0, 0, loc), 9, 0) {
		t.Error("yesterday's run + now past today's slot should be missed")
	}
	// last run was today after the slot -> not missed
	if MissedToday(now, time.Date(2026, 6, 10, 9, 30, 0, 0, loc), 9, 0) {
		t.Error("already ran today should not be missed")
	}
	// now before the slot -> not missed regardless
	early := time.Date(2026, 6, 10, 8, 0, 0, 0, loc)
	if MissedToday(early, time.Date(2026, 6, 9, 9, 0, 0, 0, loc), 9, 0) {
		t.Error("before today's slot should not be missed")
	}
	// never run (zero time) + past slot -> missed
	if !MissedToday(now, time.Time{}, 9, 0) {
		t.Error("never-run + past slot should be missed")
	}
}

func TestMissedRunFiresOnStart(t *testing.T) {
	ran := make(chan struct{}, 1)
	s, err := New("03:00", func(context.Context) { ran <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	// Pin now to just after 03:00 and lastRun to long ago so the startup
	// missed-run check fires.
	s.now = func() time.Time { return time.Date(2026, 6, 10, 3, 5, 0, 0, time.UTC) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx, time.Date(2026, 6, 1, 3, 0, 0, 0, time.UTC))
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("missed-run check did not fire job on start")
	}
}

func TestContextCancelStops(t *testing.T) {
	s, err := New("03:00", func(context.Context) {})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx, time.Now()); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
