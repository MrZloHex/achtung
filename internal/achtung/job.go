package achtung

import (
	"fmt"
	"time"
)

type JobKind int

const (
	KindTimer JobKind = iota // relative, one-shot
	KindAlarm                // absolute, one-shot
	KindEvery                // interval, repeating
	KindDaily                // wall-clock HH:MM, repeating
)

// String is the wire name for a kind. It is also what persists to disk,
// so that a stored file stays readable and survives reordering of the
// constants above.
func (k JobKind) String() string {
	switch k {
	case KindTimer:
		return "TIMER"
	case KindAlarm:
		return "ALARM"
	case KindEvery:
		return "EVERY"
	case KindDaily:
		return "DAILY"
	default:
		return "UNK"
	}
}

func ParseJobKind(s string) (JobKind, bool) {
	switch s {
	case "TIMER":
		return KindTimer, true
	case "ALARM":
		return KindAlarm, true
	case "EVERY":
		return KindEvery, true
	case "DAILY":
		return KindDaily, true
	}
	return 0, false
}

func (k JobKind) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

func (k *JobKind) UnmarshalText(b []byte) error {
	v, ok := ParseJobKind(string(b))
	if !ok {
		return fmt.Errorf("unknown job kind %q", string(b))
	}
	*k = v
	return nil
}

type Job struct {
	Name     string
	Kind     JobKind
	Due      time.Time     // next fire time (absolute)
	Interval time.Duration // for KindEvery
	AtHour   int           // for KindDaily, local wall clock
	AtMin    int           // for KindDaily, local wall clock

	Active bool // false if deleted
	Paused bool
}

// Repeating reports whether the job re-arms itself after firing.
func (j Job) Repeating() bool { return j.Kind == KindEvery || j.Kind == KindDaily }

// NextAfter returns the first fire time strictly after `now`.
//
// It deliberately skips over occurrences missed while the process was
// down rather than replaying them: waking up to three days of backlogged
// alarms is never what the user wanted. For KindEvery it also steps in
// one jump instead of looping one interval at a time, so a long outage
// cannot produce a burst of fires.
//
// One-shot kinds have no next occurrence and report false.
func (j Job) NextAfter(now time.Time) (time.Time, bool) {
	switch j.Kind {
	case KindEvery:
		if j.Interval <= 0 {
			return time.Time{}, false
		}
		if j.Due.After(now) {
			return j.Due, true
		}
		// Jump straight to the first occurrence past `now`.
		behind := now.Sub(j.Due)
		steps := behind/j.Interval + 1
		return j.Due.Add(steps * j.Interval), true

	case KindDaily:
		return nextDailyAfter(now, j.AtHour, j.AtMin), true
	}
	return time.Time{}, false
}

// nextDailyAfter returns the next local wall-clock hh:mm strictly after
// `now`. Building the time from the calendar date each time (rather than
// adding 24h) is what keeps a daily job pinned to the wall clock across
// DST transitions.
func nextDailyAfter(now time.Time, hour, min int) time.Time {
	y, mo, d := now.Date()
	next := time.Date(y, mo, d, hour, min, 0, 0, now.Location())
	if !next.After(now) {
		next = time.Date(y, mo, d+1, hour, min, 0, 0, now.Location())
	}
	return next
}
