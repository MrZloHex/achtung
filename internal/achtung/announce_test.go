package achtung

import (
	"testing"
	"time"

	"github.com/MrZloHex/monolink"
)

// JOBS.COUNT and NEXT follow the job set; NEXT empties when nothing is armed.
func TestAnnounceFollowsTheJobs(t *testing.T) {
	a := &Achtung{client: monolink.New("ACHTUNG", "ws://127.0.0.1:1")}
	a.register()
	if a.jobsCount.Get() != "0" || a.next.Get() != "" {
		t.Fatalf("fresh: JOBS.COUNT=%q NEXT=%q", a.jobsCount.Get(), a.next.Get())
	}

	soon := time.Now().Add(time.Hour).Truncate(time.Second)
	a.announce([]Job{
		{Name: "later", Kind: KindTimer, Due: soon.Add(time.Hour)},
		{Name: "soon", Kind: KindAlarm, Due: soon},
		{Name: "held", Kind: KindTimer, Due: soon.Add(-time.Minute), Paused: true},
	})
	if a.jobsCount.Get() != "2" {
		t.Errorf("JOBS.COUNT = %q, want 2 (a paused job is not armed)", a.jobsCount.Get())
	}
	if a.next.Get() != soon.Format(time.RFC3339) {
		t.Errorf("NEXT = %q, want %q", a.next.Get(), soon.Format(time.RFC3339))
	}

	a.announce(nil)
	if a.jobsCount.Get() != "0" || a.next.Get() != "" {
		t.Fatalf("emptied: JOBS.COUNT=%q NEXT=%q", a.jobsCount.Get(), a.next.Get())
	}
}
