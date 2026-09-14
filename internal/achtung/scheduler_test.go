package achtung

import (
	"testing"
	"time"
)

// A repeating job whose Due went stale during an outage must not fire once
// per missed interval when it comes back. This is the regression test for
// the burst: with the old `Due = Due.Add(Interval)` re-arm, the tick loop
// would keep popping the same job while its due time was still in the
// past, emitting a fire for every interval in the gap.
func TestSchedulerStaleRepeatingJobDoesNotBurst(t *testing.T) {
	s := NewScheduler(nil)
	defer s.Shutdown()

	const interval = 40 * time.Millisecond

	// Stale by 200 intervals. Unfixed, this fires ~200 times at once.
	if err := s.Add(Job{
		Name: "every", Kind: KindEvery,
		Interval: interval,
		Due:      time.Now().Add(-200 * interval),
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	window := 5 * interval
	deadline := time.After(window)
	fires := 0
	for done := false; !done; {
		select {
		case _, ok := <-s.Events():
			if !ok {
				done = true
				break
			}
			fires++
		case <-deadline:
			done = true
		}
	}

	// Allow generous slack for scheduling jitter, but a burst is orders of
	// magnitude larger than the window can legitimately contain.
	if maxExpected := int(window/interval) + 3; fires > maxExpected {
		t.Fatalf("fired %d times in %v (interval %v); want <= %d -- the missed-occurrence burst is back",
			fires, window, interval, maxExpected)
	}
	if fires == 0 {
		t.Fatalf("the job never fired at all")
	}
}

// A restored daily job must not fire the moment it is added just because
// its stored Due is in the past.
func TestSchedulerStaleDailyDoesNotFireOnAdd(t *testing.T) {
	s := NewScheduler(nil)
	defer s.Shutdown()

	now := time.Now()
	// Pick a wall-clock time that has already passed today, and a Due from
	// two days ago, as a restore would hand us.
	at := now.Add(-2 * time.Hour)
	if err := s.Add(Job{
		Name: "morning", Kind: KindDaily,
		AtHour: at.Hour(), AtMin: at.Minute(),
		Due: now.Add(-48 * time.Hour),
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	select {
	case ev := <-s.Events():
		t.Fatalf("stale daily job fired immediately on add: %+v", ev.Job)
	case <-time.After(150 * time.Millisecond):
	}

	j, ok := s.Get("morning")
	if !ok {
		t.Fatal("job vanished")
	}
	if !j.Due.After(time.Now()) {
		t.Fatalf("job is armed in the past: %v", j.Due)
	}
	// Next occurrence is tomorrow, since that clock time has passed today.
	if d := time.Until(j.Due); d > 24*time.Hour+time.Minute {
		t.Fatalf("armed too far out: %v", d)
	}
}

// Every mutation should publish a snapshot, so the persister can write it.
func TestSchedulerPublishesSnapshots(t *testing.T) {
	s := NewScheduler(nil)
	defer s.Shutdown()

	if err := s.Add(Job{Name: "a", Kind: KindAlarm, Due: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	select {
	case snap := <-s.Snapshots():
		if len(snap) != 1 || snap[0].Name != "a" {
			t.Fatalf("unexpected snapshot: %+v", snap)
		}
	case <-time.After(time.Second):
		t.Fatal("no snapshot after Add")
	}

	if !s.Delete("a") {
		t.Fatal("Delete reported not found")
	}
	select {
	case snap := <-s.Snapshots():
		if len(snap) != 0 {
			t.Fatalf("want empty snapshot after delete, got %+v", snap)
		}
	case <-time.After(time.Second):
		t.Fatal("no snapshot after Delete")
	}
}

// The scheduler must keep working when nothing drains Snapshots -- the
// coalescing buffer is what prevents a stalled persister from wedging the
// whole service.
func TestSchedulerSurvivesUndrainedSnapshots(t *testing.T) {
	s := NewScheduler(nil)
	defer s.Shutdown()

	for i := 0; i < 50; i++ {
		if err := s.Add(Job{
			Name: string(rune('a' + i%26)), Kind: KindAlarm,
			Due: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	done := make(chan []Job, 1)
	go func() { done <- s.List() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler wedged with an undrained snapshot channel")
	}
}
