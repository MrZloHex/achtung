package achtung

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// What one achtung keeps, and how often a job may fire.
const (
	maxJobs       = 256                  // jobs at once
	maxName       = 64                   // bytes
	minInterval   = time.Minute          // the shortest EVERY
	maxAhead      = 366 * 24 * time.Hour // the longest TIMER or EVERY
	maxAlarmAhead = 10 * maxAhead        // how far ahead an ALARM may be
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

	Active bool // kept by the scheduler; what is saved
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
// One-shot kinds have no next occurrence and report false; so does a
// repeating job that could only land in the past.
func (j Job) NextAfter(now time.Time) (time.Time, bool) {
	switch j.Kind {
	case KindEvery:
		if j.Interval <= 0 {
			return time.Time{}, false
		}
		if j.Due.After(now) {
			return j.Due, true
		}
		// Jump straight to the first occurrence past `now`. A Due so old
		// that the gap saturates, or the product wraps, lands in the past:
		// such a job starts again from now.
		behind := now.Sub(j.Due)
		next := j.Due.Add((behind/j.Interval + 1) * j.Interval)
		if !next.After(now) {
			next = now.Add(j.Interval)
		}
		return next, true

	case KindDaily:
		next := nextDailyAfter(now, j.AtHour, j.AtMin)
		return next, next.After(now)
	}
	return time.Time{}, false
}

// nextDailyAfter returns the next local wall-clock hh:mm strictly after
// `now`. Building the time from the calendar date each time (rather than
// adding 24h) is what keeps a daily job pinned to the wall clock across
// DST transitions. A time the clocks skip that day comes the gap later.
func nextDailyAfter(now time.Time, hour, min int) time.Time {
	y, mo, d := now.Date()
	next := time.Date(y, mo, d, hour, min, 0, 0, now.Location())
	if !next.After(now) {
		next = time.Date(y, mo, d+1, hour, min, 0, 0, now.Location())
	}
	return next
}

// valid says whether j is a job at all: a name, a kind, and what its kind
// needs. The limits on what NEW accepts are jobFrom's.
func (j Job) valid() error {
	if err := checkName(j.Name); err != nil {
		return err
	}
	switch j.Kind {
	case KindTimer, KindAlarm:
		if j.Due.IsZero() {
			return errors.New("no due time")
		}
	case KindEvery:
		if j.Interval <= 0 {
			return errors.New("no interval")
		}
	case KindDaily:
		if j.AtHour < 0 || j.AtHour > 23 || j.AtMin < 0 || j.AtMin > 59 {
			return fmt.Errorf("%d.%d is no time of day", j.AtHour, j.AtMin)
		}
	default:
		return fmt.Errorf("kind %d", j.Kind)
	}
	return nil
}

// checkName: a job's name is what a person calls it — at most maxName
// bytes, one line, nothing invisible or turning the text around. It is
// shown on panels as it is.
func checkName(s string) error {
	switch {
	case s == "":
		return errors.New("a job needs a name")
	case len(s) > maxName:
		return fmt.Errorf("a name is at most %d bytes", maxName)
	case !utf8.ValidString(s):
		return errors.New("a name is UTF-8")
	case strings.ContainsFunc(s, unicode.IsControl):
		return errors.New("a name is one line, without control characters")
	case strings.ContainsFunc(s, hidden):
		return errors.New("a name may not hold invisible or direction-changing characters")
	}
	return nil
}

// hidden is a character that shows as nothing, turns the text after it
// around, or breaks a line. Joiners stay — emoji are written with them.
func hidden(r rune) bool {
	if r == 0x200C || r == 0x200D || (r >= 0xE0020 && r <= 0xE007F) {
		return false
	}
	return unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}
