package achtung

import (
	"fmt"
	log "log/slog"
	"strings"
	"time"

	"github.com/MrZloHex/monolink"
)

type Achtung struct {
	client   *monolink.Client
	sched    *Scheduler
	store    *Store
	bootedAt time.Time
}

// NewAchtung starts the service. A nil store, or one with an empty path,
// disables persistence: jobs then live only as long as the process, which
// is the old behaviour.
func NewAchtung(client *monolink.Client, store *Store) *Achtung {
	a := &Achtung{
		client:   client,
		sched:    NewScheduler(),
		store:    store,
		bootedAt: time.Now(),
	}
	a.restore()
	go a.eventLoop()
	go a.persistLoop()
	return a
}

// restore re-adds the jobs saved by a previous run. A corrupt or
// unreadable file is logged and skipped rather than fatal -- losing the
// jobs is bad, but refusing to start at all is worse.
func (a *Achtung) restore() {
	if a.store == nil || a.store.Path() == "" {
		return
	}
	jobs, err := a.store.Load(time.Now())
	if err != nil {
		log.Error("RESTORE FAILED", "path", a.store.Path(), "err", err)
		return
	}
	for _, j := range jobs {
		if err := a.sched.Add(j); err != nil {
			log.Error("RESTORE ADD FAILED", "name", j.Name, "err", err)
			continue
		}
		log.Info("RESTORED", "kind", kindStr(j.Kind), "name", j.Name, "due", j.Due.Format(time.DateTime))
	}
	if len(jobs) > 0 {
		log.Info("RESTORED JOBS", "count", len(jobs), "path", a.store.Path())
	}
}

// persistLoop writes the job set out after every change. The channel
// coalesces, so a burst of changes costs one write.
func (a *Achtung) persistLoop() {
	if a.store == nil || a.store.Path() == "" {
		// Still drain, or the scheduler's send would block on a full buffer.
		for range a.sched.Snapshots() {
		}
		return
	}
	for jobs := range a.sched.Snapshots() {
		if err := a.store.Save(jobs); err != nil {
			log.Error("PERSIST FAILED", "path", a.store.Path(), "err", err)
			continue
		}
		log.Debug("PERSISTED", "count", len(jobs), "path", a.store.Path())
	}
}

func (a *Achtung) Shutdown() { a.sched.Shutdown() }

// Cmd dispatches an incoming request by verb.
//
//	PING PING               -> PONG PONG (v1) | OK PING (v2)
//	NEW  TIMER <name> <dur> -> OK TIMER <name>
//	NEW  ALARM <name> <d> <t> -> OK ALARM <name>
//	NEW  EVERY <name> <dur> -> OK EVERY <name>
//	NEW  DAILY <name> <H.M> -> OK DAILY <name>
//	STOP TIMER|ALARM|EVERY|DAILY <name> -> OK <kind> <name>
//	GET  LIST               -> OK LIST [<kind> <name>...]
//	GET  JOB <name>         -> OK JOB <kind> <name> <rem> <due>
//	GET  UPTIME             -> OK UPTIME <dur>
func (a *Achtung) Cmd(req *monolink.Request) {
	msg := req.Msg
	log.Debug("CMD", "from", msg.From, "verb", msg.Verb, "noun", msg.Noun, "args", msg.Args)

	switch msg.Verb {
	case "OK", "ERR", "PONG":
		log.Debug("IGNORE", "verb", msg.Verb, "noun", msg.Noun, "from", msg.From)
		return
	case "PING":
		// v2 answers OK:PING, correlated by id like any reply (SPEC §43).
		if msg.Version == monolink.V2 {
			req.Reply("OK", "PING")
		} else {
			req.Reply("PONG", "PONG")
		}
	case "NEW":
		a.cmdNew(req)
	case "STOP":
		a.cmdStop(req)
	case "GET":
		a.cmdGet(req)
	default:
		log.Warn("UNKNOWN VERB", "verb", msg.Verb, "from", msg.From)
		req.Reply("ERR", "VERB")
	}
}

func (a *Achtung) cmdNew(req *monolink.Request) {
	msg := req.Msg
	switch msg.Noun {
	case "TIMER":
		if len(msg.Args) < 2 {
			req.Reply("ERR", "ARGC")
			return
		}
		name := msg.Args[0]
		d, err := time.ParseDuration(msg.Args[1])
		if err != nil {
			log.Warn("BAD DURATION", "raw", msg.Args[1], "from", msg.From)
			req.Reply("ERR", "DUR")
			return
		}
		job := Job{
			Name: name, Kind: KindTimer,
			Due: time.Now().Add(d),
		}
		if err := a.sched.Add(job); err != nil {
			log.Error("ADD FAILED", "name", name, "err", err)
			req.Reply("ERR", "ADD", err.Error())
			return
		}
		log.Info("NEW TIMER", "name", name, "duration", d, "due", job.Due.Format(time.DateTime), "from", msg.From)
		req.Reply("OK", "TIMER", name)

	case "ALARM":
		if len(msg.Args) < 3 {
			req.Reply("ERR", "ARGC")
			return
		}
		name := msg.Args[0]
		tm, err := parseTimeLocal(msg.Args[1], msg.Args[2])
		if err != nil {
			log.Warn("BAD TIME", "date", msg.Args[1], "time", msg.Args[2], "from", msg.From)
			req.Reply("ERR", "TIME", msg.Args[1], msg.Args[2])
			return
		}
		job := Job{
			Name: name, Kind: KindAlarm,
			Due: tm,
		}
		if err := a.sched.Add(job); err != nil {
			log.Error("ADD FAILED", "name", name, "err", err)
			req.Reply("ERR", "ADD", err.Error())
			return
		}
		log.Info("NEW ALARM", "name", name, "due", tm.Format(time.DateTime), "from", msg.From)
		req.Reply("OK", "ALARM", name)

	case "EVERY":
		if len(msg.Args) < 2 {
			req.Reply("ERR", "ARGC")
			return
		}
		name := msg.Args[0]
		d, err := time.ParseDuration(msg.Args[1])
		if err != nil || d <= 0 {
			log.Warn("BAD INTERVAL", "raw", msg.Args[1], "from", msg.From)
			req.Reply("ERR", "DUR")
			return
		}
		job := Job{
			Name: name, Kind: KindEvery,
			Due:      time.Now().Add(d),
			Interval: d,
		}
		if err := a.sched.Add(job); err != nil {
			log.Error("ADD FAILED", "name", name, "err", err)
			req.Reply("ERR", "ADD", err.Error())
			return
		}
		log.Info("NEW EVERY", "name", name, "interval", d, "due", job.Due.Format(time.DateTime), "from", msg.From)
		req.Reply("OK", "EVERY", name)

	case "DAILY":
		if len(msg.Args) < 2 {
			req.Reply("ERR", "ARGC")
			return
		}
		name := msg.Args[0]
		hour, minute, err := parseClockLocal(msg.Args[1])
		if err != nil {
			log.Warn("BAD CLOCK", "raw", msg.Args[1], "from", msg.From)
			req.Reply("ERR", "TIME", msg.Args[1])
			return
		}
		job := Job{
			Name: name, Kind: KindDaily,
			AtHour: hour, AtMin: minute,
			Due: nextDailyAfter(time.Now(), hour, minute),
		}
		if err := a.sched.Add(job); err != nil {
			log.Error("ADD FAILED", "name", name, "err", err)
			req.Reply("ERR", "ADD", err.Error())
			return
		}
		log.Info("NEW DAILY", "name", name, "at", msg.Args[1], "due", job.Due.Format(time.DateTime), "from", msg.From)
		req.Reply("OK", "DAILY", name)

	default:
		log.Warn("UNKNOWN NOUN", "noun", msg.Noun, "from", msg.From)
		req.Reply("ERR", "NOUN")
	}
}

func (a *Achtung) cmdStop(req *monolink.Request) {
	msg := req.Msg
	switch msg.Noun {
	case "TIMER", "ALARM", "EVERY", "DAILY":
		if len(msg.Args) < 1 {
			req.Reply("ERR", "ARGC")
			return
		}
		name := msg.Args[0]
		ok := a.sched.Delete(name)
		if !ok {
			log.Warn("STOP NOT FOUND", "kind", msg.Noun, "name", name, "from", msg.From)
		} else {
			log.Info("STOP", "kind", msg.Noun, "name", name, "from", msg.From)
		}
		a.client.Send("VERTEX", "OFF", "BUZZ")
		req.Reply("OK", msg.Noun, name)

	default:
		log.Warn("UNKNOWN NOUN", "noun", msg.Noun, "from", msg.From)
		req.Reply("ERR", "NOUN")
	}
}

func (a *Achtung) cmdGet(req *monolink.Request) {
	msg := req.Msg
	switch msg.Noun {
	case "LIST":
		jobs := a.sched.List()
		var parts []string
		for _, j := range jobs {
			if !j.Active {
				continue
			}
			parts = append(parts, kindStr(j.Kind), j.Name)
		}
		log.Debug("GET LIST", "count", len(parts)/2, "from", msg.From)
		if len(parts) == 0 {
			req.Reply("OK", "LIST")
			return
		}
		req.Reply("OK", "LIST", parts...)

	case "UPTIME":
		uptime := time.Since(a.bootedAt).Truncate(time.Second)
		log.Debug("GET UPTIME", "uptime", uptime, "from", msg.From)
		req.Reply("OK", "UPTIME", uptime.String())

	case "JOB":
		if len(msg.Args) < 1 {
			req.Reply("ERR", "ARGC")
			return
		}
		name := msg.Args[0]
		j, ok := a.sched.Get(name)
		if !ok || !j.Active {
			log.Debug("GET JOB NOT FOUND", "name", name, "from", msg.From)
			req.Reply("ERR", "NAC")
			return
		}
		rem := time.Until(j.Due).Truncate(time.Second)
		if rem < 0 {
			rem = 0
		}
		log.Debug("GET JOB", "name", name, "kind", kindStr(j.Kind), "remaining", rem, "from", msg.From)
		req.Reply("OK", "JOB", kindStr(j.Kind), j.Name, rem.String(), serializeTimeLocal(j.Due))

	default:
		log.Warn("UNKNOWN NOUN", "noun", msg.Noun, "from", msg.From)
		req.Reply("ERR", "NOUN")
	}
}

func (a *Achtung) eventLoop() {
	for ev := range a.sched.Events() {
		j := ev.Job
		kind := kindStr(j.Kind)
		log.Info("FIRE", "kind", kind, "name", j.Name)
		a.client.Send("ALL", "FIRE", kind, j.Name)
		a.client.Send("VERTEX", "ON", "BUZZ")
	}
}

func kindStr(k JobKind) string { return k.String() }

// parseClockLocal reads a wire clock time. The wire has no colons inside
// a field, so "H.M" is the canonical form; "H:M" is accepted too for
// anything typed by hand.
func parseClockLocal(s string) (hour, minute int, err error) {
	norm := strings.ReplaceAll(s, ":", ".")
	if _, err = fmt.Sscanf(norm, "%d.%d", &hour, &minute); err != nil {
		return 0, 0, err
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("clock out of range: %q", s)
	}
	return hour, minute, nil
}

func parseTimeLocal(d, t string) (time.Time, error) {
	var year, month, day, hour, minute int
	_, err := fmt.Sscanf(d, "%d.%d.%d", &year, &month, &day)
	if err != nil {
		return time.Time{}, err
	}
	_, err = fmt.Sscanf(t, "%d.%d", &hour, &minute)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(year, time.Month(month), day, hour, minute, 0, 0, time.Local), nil
}

// serializeTimeLocal renders a due time as a single wire token. It has to
// be one field: OK:JOB is specified as
// OK:JOB:<kind>:<name>:<remaining>:<due>, and a colon here would split the
// due into two, pushing FROM out of position for every reader.
// Zero-padded so a reader can use one fixed layout ("2006.01.02.15.04")
// instead of guessing at field widths.
func serializeTimeLocal(t time.Time) string {
	return fmt.Sprintf("%04d.%02d.%02d.%02d.%02d",
		t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute())
}
