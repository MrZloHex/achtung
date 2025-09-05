package achtung

import (
	"time"
)

type JobKind int

const (
	KindTimer JobKind = iota // relative, one-shot
	KindAlarm                // absolute, one-shot
	KindEvery                // interval, repeating
)

type Job struct {
	Name     string
	From     string
	Kind     JobKind
	Due      time.Time     // next fire time (absolute)
	Interval time.Duration // for KindEvery
	Data     []string

	Active bool // false if deleted
	Paused bool
}
