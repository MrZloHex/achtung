package achtung

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrZloHex/monolink"
)

// The findings of the review of 2026-09-13 (its report is kept outside the
// repository), each as the invariant that holds now.

func unconnected() *monolink.Client { return monolink.New(NodeName, "ws://127.0.0.1:1") }

// 2: a fired job reaches its reader however long the reader is away.
func TestNoFireIsLostToABusyReader(t *testing.T) {
	s := NewScheduler(nil)
	defer s.Shutdown()
	due := time.Now().Add(-time.Second)
	for i := range 100 {
		if err := s.Add(Job{Name: fmt.Sprintf("j%03d", i), Kind: KindAlarm, Due: due}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(100 * time.Millisecond) // the reader is away; the jobs fire
	for got := 0; got < 100; got++ {
		select {
		case <-s.Events():
		case <-time.After(2 * time.Second):
			t.Fatalf("%d of 100 fires reached the reader", got)
		}
	}
}

// 3: a job replaced or stopped leaves nothing behind in the heap.
func TestAReplacedJobLeavesNoTrace(t *testing.T) {
	s := NewScheduler(nil)
	if err := s.Add(Job{Name: "anchor", Kind: KindAlarm, Due: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for i := range 1000 {
		if err := s.Add(Job{Name: "x", Kind: KindAlarm, Due: time.Now().Add(2*time.Hour + time.Duration(i)*time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if !s.Delete("anchor") {
		t.Fatal("anchor not found")
	}
	s.Shutdown() // the loop has stopped: its state may be read
	if len(s.h) != 1 || len(s.idx) != 1 {
		t.Fatalf("%d in the heap, %d by name; want 1 and 1", len(s.h), len(s.idx))
	}
	for i, it := range s.h {
		if it.idx != i {
			t.Fatalf("heap item %d thinks it is at %d", i, it.idx)
		}
	}
}

// 5, 16: after Shutdown every call returns at once, and twice is harmless.
func TestShutdownIsFinal(t *testing.T) {
	s := NewScheduler(nil)
	s.Shutdown()
	s.Shutdown()
	done := make(chan error, 1)
	go func() { done <- s.Add(Job{Name: "late", Kind: KindTimer, Due: time.Now().Add(time.Hour)}) }()
	select {
	case err := <-done:
		if !errors.Is(err, errClosed) {
			t.Fatalf("an add after shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a call after shutdown hung")
	}
	if _, ok := s.Get("late"); ok || s.List() != nil || s.Delete("late") {
		t.Fatal("a closed scheduler still answered")
	}

	a, err := NewAchtung(unconnected(), nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Shutdown()
	a.Shutdown()
}

// 14: a change whose save fails is undone, and says so.
func TestAFailedSaveUndoesTheChange(t *testing.T) {
	var fail atomic.Bool
	s := NewScheduler(func([]Job) error {
		if fail.Load() {
			return errors.New("disk full")
		}
		return nil
	})
	defer s.Shutdown()
	keep := Job{Name: "keep", Kind: KindAlarm, Due: time.Now().Add(time.Hour)}
	if err := s.Add(keep); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	if err := s.Add(Job{Name: "new", Kind: KindAlarm, Due: time.Now().Add(time.Hour)}); err == nil {
		t.Fatal("an unsaved add reported success")
	}
	if err := s.Add(Job{Name: "keep", Kind: KindAlarm, Due: time.Now().Add(3 * time.Hour)}); err == nil {
		t.Fatal("an unsaved replacement reported success")
	}
	if err := s.Stop("keep", KindAlarm); err == nil {
		t.Fatal("an unsaved stop reported success")
	}
	jobs := s.List()
	if len(jobs) != 1 || jobs[0].Name != "keep" || !jobs[0].Due.Equal(keep.Due) {
		t.Fatalf("after failed saves: %+v", jobs)
	}
}

// 9: STOP names the kind it stops.
func TestStopChecksTheKind(t *testing.T) {
	s := NewScheduler(nil)
	defer s.Shutdown()
	if err := s.Add(Job{Name: "morning", Kind: KindDaily, AtHour: 7, Due: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop("morning", KindTimer); !errors.Is(err, errWrongKind) {
		t.Fatalf("STOP TIMER on a DAILY: %v", err)
	}
	if _, ok := s.Get("morning"); !ok {
		t.Fatal("STOP TIMER removed a DAILY")
	}
	if err := s.Stop("morning", KindDaily); err != nil {
		t.Fatal(err)
	}
}

// 9: a job fired before a STOP does not sound the buzzer after it.
func TestAStopIsNotUndoneByAFireOnItsWay(t *testing.T) {
	a := &Achtung{client: unconnected()}
	before := Event{Job: Job{Name: "x"}, FiredAt: time.Now()}
	a.silence() // not connected: the buzzer is not told, but the STOP is kept
	if err := a.buzz(before); err != nil {
		t.Fatalf("a fire from before the STOP went for the buzzer: %v", err)
	}
	after := Event{Job: Job{Name: "y"}, FiredAt: time.Now().Add(time.Millisecond)}
	if err := a.buzz(after); err == nil {
		t.Fatal("a fire after the STOP did not go for the buzzer")
	}
}

// 6: a fire the bus cannot take is tried again — not forever, and not past
// shutdown.
func TestAFireTheBusCannotTakeIsGivenUp(t *testing.T) {
	a := &Achtung{client: unconnected(), stop: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		a.deliver(Event{Job: Job{Name: "old", Kind: KindTimer}, FiredAt: time.Now().Add(-lateFire)})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * retryEvery):
		t.Fatal("a fire missed long ago was tried on")
	}

	done = make(chan struct{})
	go func() {
		a.deliver(Event{Job: Job{Name: "fresh", Kind: KindTimer}, FiredAt: time.Now()})
		close(done)
	}()
	close(a.stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a fire was tried on past shutdown")
	}
}

// 7: LIST comes as much as a frame holds, and paging lists every job once.
func TestTheListComesInPages(t *testing.T) {
	a := &Achtung{sched: NewScheduler(nil)}
	defer a.sched.Shutdown()
	for i := range 20 {
		if err := a.sched.Add(Job{Name: fmt.Sprintf("job:%02d %s", i, strings.Repeat("n", 50)), Kind: KindTimer, Due: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	after := ""
	for pages := 0; ; pages++ {
		page := a.listAfter(after)
		if len(page) == 0 {
			break
		}
		m := monolink.Message{Version: monolink.V2, ID: "0123abcd", From: NodeName, To: "MONOWEB.someone", Verb: "OK", Noun: "LIST", Args: page}
		if _, err := m.Marshal(); err != nil {
			t.Fatalf("a page does not fit a frame: %v", err)
		}
		for i := 0; i < len(page); i += 2 {
			if seen[page[i+1]] {
				t.Fatalf("%q listed twice", page[i+1])
			}
			seen[page[i+1]] = true
			after = page[i+1]
		}
		if pages > 10 {
			t.Fatal("the pages never end")
		}
	}
	if len(seen) != 20 {
		t.Fatalf("%d of 20 jobs listed", len(seen))
	}
}

// 4, 8: what NEW refuses.
func TestWhatAJobCannotBe(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.Local)
	for name, c := range map[string]struct {
		noun string
		args []string
	}{
		"an overflowing date":  {"ALARM", []string{"a", "2026.13.32", "25.61junk"}},
		"a date with a tail":   {"ALARM", []string{"a", "2026.09.14junk", "07.30"}},
		"an alarm in the past": {"ALARM", []string{"a", "2026.09.13", "11.59"}},
		"an alarm too far":     {"ALARM", []string{"a", "2040.01.01", "07.30"}},
		"a clock with a tail":  {"DAILY", []string{"a", "07.30junk"}},
		"no time of day":       {"DAILY", []string{"a", "24.00"}},
		"a timer of nothing":   {"TIMER", []string{"a", "0s"}},
		"a timer backwards":    {"TIMER", []string{"a", "-1m"}},
		"a timer for years":    {"TIMER", []string{"a", "9000h"}},
		"a nanosecond repeat":  {"EVERY", []string{"a", "1ns"}},
		"too often":            {"EVERY", []string{"a", "30s"}},
		"no name":              {"TIMER", []string{" ", "1m"}},
		"a name on two lines":  {"TIMER", []string{"a\nb", "1m"}},
		"a long name":          {"TIMER", []string{strings.Repeat("n", maxName+1), "1m"}},
		"too many arguments":   {"TIMER", []string{"a", "1m", "x"}},
		"no such kind":         {"SOON", []string{"a", "1m"}},
	} {
		if _, err := jobFrom(c.noun, c.args, now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if j, err := jobFrom("ALARM", []string{"wake", "2026.9.14", "7.05"}, now); err != nil || !j.Due.Equal(time.Date(2026, 9, 14, 7, 5, 0, 0, time.Local)) {
		t.Errorf("an alarm: %+v, %v", j, err)
	}
	if j, err := jobFrom("DAILY", []string{"morning", "7:30"}, now); err != nil || j.AtHour != 7 || j.AtMin != 30 {
		t.Errorf("a daily job typed with a colon: %+v, %v", j, err)
	}
	if j, err := jobFrom("EVERY", []string{"tea", "1h"}, now); err != nil || j.Interval != time.Hour {
		t.Errorf("a repeat: %+v, %v", j, err)
	}
}

// 13: repeating arithmetic lands ahead of now, however old the job.
func TestAnAncientJobStillLandsAhead(t *testing.T) {
	now := time.Now()
	for _, j := range []Job{
		{Kind: KindEvery, Interval: time.Minute, Due: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Kind: KindEvery, Interval: maxAhead, Due: time.Date(1700, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		if next, ok := j.NextAfter(now); !ok || !next.After(now) {
			t.Errorf("every %v from %v: next %v", j.Interval, j.Due, next)
		}
	}
	if next, ok := (Job{Kind: KindDaily, AtHour: -1000}).NextAfter(now); ok && !next.After(now) {
		t.Fatal("a daily job was re-armed into the past")
	}
}

// 13, 14: a saved file that cannot be right stops achtung, the file
// untouched.
func TestABrokenJobsFileDoesNotLoad(t *testing.T) {
	future := `"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"`
	var many []string
	for i := range maxJobs + 1 {
		many = append(many, fmt.Sprintf(`{"Name":"j%d","Kind":"TIMER","Due":F,"Active":true}`, i))
	}
	for name, body := range map[string]string{
		"not json":       `[`,
		"twice":          `[{"Name":"a","Kind":"TIMER","Due":F,"Active":true},{"Name":"a","Kind":"TIMER","Due":F,"Active":true}]`,
		"no name":        `[{"Name":"","Kind":"TIMER","Due":F,"Active":true}]`,
		"no time of day": `[{"Name":"a","Kind":"DAILY","AtHour":-5,"Due":F,"Active":true}]`,
		"too often":      `[{"Name":"a","Kind":"EVERY","Interval":1,"Due":F,"Active":true}]`,
		"unknown kind":   `[{"Name":"a","Kind":"SOON","Due":F,"Active":true}]`,
		"too many":       "[" + strings.Join(many, ",") + "]",
		"too large":      strings.Repeat(" ", maxStateBytes+1) + "[]",
	} {
		path := filepath.Join(t.TempDir(), "jobs.json")
		body = strings.ReplaceAll(body, "F", future)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(path).Load(time.Now()); err == nil {
			t.Errorf("%s: loaded", name)
		}
		if _, err := NewAchtung(unconnected(), NewStore(path)); err == nil {
			t.Errorf("%s: achtung started on it", name)
		}
		if raw, _ := os.ReadFile(path); string(raw) != body {
			t.Errorf("%s: the file was changed", name)
		}
	}
}

// 14: a directory that cannot be synced fails a save before the file is
// replaced; the file is replaced whole or not at all, readable by its owner.
func TestASaveThatCannotSyncChangesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")
	s := NewStore(path)
	if err := s.Save([]Job{{Name: "a", Kind: KindTimer, Due: time.Now().Add(time.Hour), Active: true}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	err := s.Save(nil)
	os.Chmod(dir, 0o700)
	if err == nil {
		t.Fatal("a save into an unreadable directory reported success")
	}
	if jobs, err := s.Load(time.Now()); err != nil || len(jobs) != 1 {
		t.Fatalf("the file after a failed save: %v, %v", jobs, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("jobs.json is %v", fi.Mode().Perm())
	}
}

// What the object model answers, achtung leaves alone: one answer each.
func TestEachRequestHasOneAnswerer(t *testing.T) {
	for _, c := range []struct {
		verb, noun string
		elsewhere  bool
	}{
		{"PING", "PING", true}, {"GET", "UPTIME", true}, {"GET", "JOBS.COUNT", true}, {"GET", "REG", true},
		{"PUB", "NEXT", true}, {"OK", "LIST", true},
		{"GET", "LIST", false}, {"GET", "JOB", false}, {"NEW", "TIMER", false}, {"STOP", "ALARM", false}, {"DO", "X", false},
	} {
		if got := answeredElsewhere(monolink.Message{Verb: c.verb, Noun: c.noun}); got != c.elsewhere {
			t.Errorf("%s %s: elsewhere %v, want %v", c.verb, c.noun, got, c.elsewhere)
		}
	}
}
