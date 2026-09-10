package achtung

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNextAfterEveryDoesNotBurstAfterOutage(t *testing.T) {
	// A job that should have fired every hour, last armed three days ago.
	// The next fire must be a single step into the future, not one per
	// missed hour.
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.Local)
	j := Job{
		Name: "hourly", Kind: KindEvery,
		Interval: time.Hour,
		Due:      now.Add(-72 * time.Hour),
	}

	next, ok := j.NextAfter(now)
	if !ok {
		t.Fatal("NextAfter returned !ok for KindEvery")
	}
	if !next.After(now) {
		t.Fatalf("next fire %v is not after now %v", next, now)
	}
	if got := next.Sub(now); got > time.Hour {
		t.Fatalf("next fire is %v away, want <= 1h (did not jump the gap)", got)
	}
	// And it must stay on the original phase.
	if next.Minute() != j.Due.Minute() {
		t.Fatalf("phase drifted: due minute %d, next minute %d", j.Due.Minute(), next.Minute())
	}
}

func TestNextAfterDailyIsWallClock(t *testing.T) {
	at := func(h, m int) Job {
		return Job{Name: "morning", Kind: KindDaily, AtHour: h, AtMin: m}
	}

	// Before today's time -> today.
	now := time.Date(2026, 9, 10, 6, 0, 0, 0, time.Local)
	next, _ := at(7, 0).NextAfter(now)
	if next.Day() != 10 || next.Hour() != 7 || next.Minute() != 0 {
		t.Fatalf("want 2026-09-10 07:00, got %v", next)
	}

	// After today's time -> tomorrow.
	now = time.Date(2026, 9, 10, 8, 0, 0, 0, time.Local)
	next, _ = at(7, 0).NextAfter(now)
	if next.Day() != 11 || next.Hour() != 7 {
		t.Fatalf("want 2026-09-11 07:00, got %v", next)
	}

	// Exactly at the time -> tomorrow, never the same instant twice.
	now = time.Date(2026, 9, 10, 7, 0, 0, 0, time.Local)
	next, _ = at(7, 0).NextAfter(now)
	if !next.After(now) {
		t.Fatalf("next %v must be strictly after now %v", next, now)
	}
	if next.Day() != 11 {
		t.Fatalf("want next day, got %v", next)
	}

	// A daily job stale by three days still lands tomorrow at the hour,
	// not three fires deep.
	now = time.Date(2026, 9, 10, 9, 0, 0, 0, time.Local)
	j := at(7, 30)
	j.Due = time.Date(2026, 9, 7, 7, 30, 0, 0, time.Local)
	next, _ = j.NextAfter(now)
	if next.Day() != 11 || next.Hour() != 7 || next.Minute() != 30 {
		t.Fatalf("want 2026-09-11 07:30, got %v", next)
	}
}

func TestNextAfterOneShotHasNoNext(t *testing.T) {
	now := time.Now()
	for _, k := range []JobKind{KindTimer, KindAlarm} {
		if _, ok := (Job{Kind: k, Due: now}).NextAfter(now); ok {
			t.Fatalf("%v reported a next occurrence", k)
		}
	}
}

func TestStoreRoundTripAndMissedJobPolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")
	s := NewStore(path)

	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.Local)
	in := []Job{
		{Name: "future-alarm", Kind: KindAlarm, Due: now.Add(time.Hour), Active: true},
		{Name: "missed-timer", Kind: KindTimer, Due: now.Add(-time.Hour), Active: true},
		{Name: "morning", Kind: KindDaily, AtHour: 7, AtMin: 0, Due: now.Add(-48 * time.Hour), Active: true},
		{Name: "deleted", Kind: KindAlarm, Due: now.Add(time.Hour), Active: false},
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := s.Load(now)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := map[string]Job{}
	for _, j := range out {
		got[j.Name] = j
	}

	if _, ok := got["missed-timer"]; ok {
		t.Error("a one-shot that expired while down was replayed; it should be dropped")
	}
	if _, ok := got["deleted"]; ok {
		t.Error("an inactive job was persisted and restored")
	}
	if j, ok := got["future-alarm"]; !ok {
		t.Error("a still-pending one-shot was lost")
	} else if !j.Due.Equal(in[0].Due) {
		t.Errorf("due time changed across save/load: %v -> %v", in[0].Due, j.Due)
	}
	if j, ok := got["morning"]; !ok {
		t.Error("the daily job was lost")
	} else {
		if !j.Due.After(now) {
			t.Errorf("daily job restored with a stale due %v", j.Due)
		}
		if j.Due.Hour() != 7 || j.Due.Minute() != 0 {
			t.Errorf("daily job lost its wall-clock time: %v", j.Due)
		}
		if j.AtHour != 7 || j.AtMin != 0 {
			t.Errorf("AtHour/AtMin did not survive the round trip: %d:%d", j.AtHour, j.AtMin)
		}
	}
}

func TestStoreKindsPersistByName(t *testing.T) {
	// The file should stay readable, and stay correct if the JobKind
	// constants are ever reordered.
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")
	s := NewStore(path)

	now := time.Now()
	if err := s.Save([]Job{{Name: "x", Kind: KindDaily, AtHour: 7, Due: now.Add(time.Hour), Active: true}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(b), `"Kind": "DAILY"`) {
		t.Fatalf("kind not stored by name:\n%s", b)
	}
}

func TestStoreMissingFileIsNotAnError(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "does-not-exist.json"))
	jobs, err := s.Load(time.Now())
	if err != nil {
		t.Fatalf("a first run with no state file must not error: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("want no jobs, got %d", len(jobs))
	}
}

func TestStoreDisabledWhenPathEmpty(t *testing.T) {
	s := NewStore("")
	if err := s.Save([]Job{{Name: "x", Active: true}}); err != nil {
		t.Fatalf("Save with empty path should be a no-op: %v", err)
	}
	jobs, err := s.Load(time.Now())
	if err != nil || len(jobs) != 0 {
		t.Fatalf("Load with empty path should be a no-op: %v %v", jobs, err)
	}
}

func TestParseClockLocal(t *testing.T) {
	ok := map[string][2]int{
		"7.0":   {7, 0},
		"07.30": {7, 30},
		"23.59": {23, 59},
		"7:15":  {7, 15}, // typed by hand
	}
	for in, want := range ok {
		h, m, err := parseClockLocal(in)
		if err != nil {
			t.Errorf("%q: unexpected error %v", in, err)
			continue
		}
		if h != want[0] || m != want[1] {
			t.Errorf("%q: got %d:%d, want %d:%d", in, h, m, want[0], want[1])
		}
	}
	for _, in := range []string{"24.00", "7.60", "-1.0", "banana", ""} {
		if _, _, err := parseClockLocal(in); err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
}

func TestSerializeTimeLocalHasNoColon(t *testing.T) {
	// OK:JOB is TO:VERB:NOUN:kind:name:remaining:due:FROM. A colon in the
	// due field shifts FROM out of position for every reader.
	got := serializeTimeLocal(time.Date(2026, 9, 10, 7, 5, 0, 0, time.Local))
	if contains(got, ":") {
		t.Fatalf("due token %q contains a colon", got)
	}
	if got != "2026.09.10.07.05" {
		t.Fatalf("got %q, want 2026.09.10.07.05", got)
	}
	// One fixed layout must parse it.
	if _, err := time.ParseInLocation("2006.01.02.15.04", got, time.Local); err != nil {
		t.Fatalf("due token %q is not parseable with a fixed layout: %v", got, err)
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
