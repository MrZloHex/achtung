package achtung

import (
	"errors"
	"fmt"
	log "log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MrZloHex/monolink"
)

// NodeName is achtung's address on the bus.
const NodeName = "ACHTUNG"

const (
	// lateFire is how long after firing a job is still worth telling the
	// bus and the buzzer: a hub restarting is waited out, but an alarm an
	// hour late is a different alarm.
	lateFire   = 10 * time.Minute
	retryEvery = 2 * time.Second
	// listPage is how many jobs one LIST reply carries: a kind and a name
	// each, sixteen arguments to a frame (SPEC §15).
	listPage = 8
)

// Achtung serves whoever the hub lets through: the hub holds every panel to
// its person's grants (ACHTUNG.NEW.*, ACHTUNG.STOP.*, ACHTUNG.GET.*) and
// every node to its policy line (SECURITY.txt §5). Jobs belong to the
// household, not to whoever made them.
type Achtung struct {
	client   *monolink.Client
	sched    *Scheduler
	store    *Store
	bootedAt time.Time

	jobsCount, next *monolink.Property // what achtung owes the bus, SPEC §27

	buzzMu   sync.Mutex
	silenced time.Time // the last STOP: no job fired before it sounds the buzzer

	stop     chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewAchtung restores the saved jobs and starts the service. A nil store,
// or one with an empty path, keeps jobs only as long as the process.
//
// A saved file that cannot be read, or is not right, stops achtung with the
// file untouched: carrying on without it, the next change would write over
// it with whatever was left.
func NewAchtung(client *monolink.Client, store *Store) (*Achtung, error) {
	var jobs []Job
	var persist func([]Job) error
	if store != nil && store.Path() != "" {
		var err error
		if jobs, err = store.Load(time.Now()); err != nil {
			return nil, err
		}
		persist = store.Save
	}
	a := &Achtung{
		client:   client,
		sched:    NewScheduler(persist),
		store:    store,
		bootedAt: time.Now(),
		stop:     make(chan struct{}),
	}
	a.register()
	for _, j := range jobs {
		if err := a.sched.Add(j); err != nil {
			a.sched.Shutdown()
			return nil, fmt.Errorf("restore %q: %w", j.Name, err)
		}
		log.Info("RESTORED", "kind", j.Kind, "name", j.Name, "due", j.Due.Format(time.DateTime))
	}
	a.wg.Add(2)
	go a.eventLoop()
	go a.announceLoop()
	return a, nil
}

// register describes achtung to the bus: REG on every connect, then a PUB
// whenever a property moves. Before the first Connect, so that connect
// announces it.
func (a *Achtung) register() {
	node := monolink.NewNode(a.client, monolink.NodeInfo{
		Class: monolink.ClassNode, Product: "achtung", Version: monolink.BuildVersion(),
	})
	a.jobsCount = node.Prop("JOBS.COUNT", monolink.Int(0, maxJobs), "jobs armed")
	a.next = node.Prop("NEXT", monolink.Time(), "when the next job fires; empty when none is")
	a.announce(nil)
}

// announce sets what achtung owes from a job set: how many are armed, and
// when the soonest fires. A paused job is not armed.
func (a *Achtung) announce(jobs []Job) {
	if a.jobsCount == nil {
		return
	}
	armed := 0
	var next time.Time
	for _, j := range jobs {
		if j.Paused {
			continue
		}
		armed++
		if next.IsZero() || j.Due.Before(next) {
			next = j.Due
		}
	}
	a.jobsCount.Set(strconv.Itoa(armed))
	if next.IsZero() {
		a.next.Set("")
	} else {
		a.next.Set(next.Format(time.RFC3339))
	}
}

// announceLoop follows every change to the job set, off the scheduler's
// goroutine: announcing is the bus's business, and may wait on it.
func (a *Achtung) announceLoop() {
	defer a.wg.Done()
	for jobs := range a.sched.Snapshots() {
		a.announce(jobs)
	}
}

// Shutdown stops firing and taking changes — every change answered is
// saved already — stops waiting on the bus for fires it has not taken,
// and waits for both loops. Close the client after. Twice is harmless.
func (a *Achtung) Shutdown() {
	a.stopOnce.Do(func() {
		close(a.stop)
		a.sched.Shutdown()
		a.wg.Wait()
	})
}

// Serve answers requests from inbox — the client's — one at a time, in the
// order they arrived: monolink runs each handler on its own goroutine, and
// a NEW then a STOP must not be done the other way round. It returns when
// the inbox closes, with the client.
func (a *Achtung) Serve(inbox <-chan monolink.Message) {
	for m := range inbox {
		a.Cmd(m)
	}
}

// Cmd answers one request. Arguments are never logged whole.
//
//	NEW  TIMER <name> <dur>      -> OK TIMER <name>
//	NEW  ALARM <name> <d> <t>    -> OK ALARM <name>
//	NEW  EVERY <name> <dur>      -> OK EVERY <name>
//	NEW  DAILY <name> <H.M>      -> OK DAILY <name>
//	STOP TIMER|ALARM|EVERY|DAILY <name> -> OK <kind> <name>
//	GET  LIST [<after>]          -> OK LIST [<kind> <name>...]  by name; empty past the last
//	GET  JOB <name>              -> OK JOB <kind> <name> <rem> <due>
//
// PING, UPTIME, VERSION, JOBS.COUNT, NEXT and the roll call are the object
// model's (monolink.Node).
func (a *Achtung) Cmd(m monolink.Message) {
	if m.Version != monolink.V2 {
		return // the hub carries v2 alone
	}
	to, err := monolink.ParseAddress(m.To)
	if err != nil || to.Node != NodeName || to.Bubble != "" || answeredElsewhere(m) {
		return
	}
	log.Debug("CMD", "from", m.From, "verb", m.Verb, "noun", m.Noun)
	var (
		noun string
		args []string
	)
	switch m.Verb {
	case monolink.VerbNew:
		noun, args, err = a.cmdNew(m)
	case monolink.VerbStop:
		noun, args, err = a.cmdStop(m)
	case monolink.VerbGet:
		noun, args, err = a.cmdGet(m)
	default:
		err = monolink.Fail(monolink.CodeVerb, "achtung takes NEW, STOP and GET")
	}
	if err != nil {
		a.fail(m, err)
		return
	}
	a.reply(m, monolink.VerbOK, noun, args...)
}

// answeredElsewhere is whether m is not achtung's to answer: the object
// model answers PING, the roll call and achtung's properties, and nobody
// answers a reply or an announcement (§18).
func answeredElsewhere(m monolink.Message) bool {
	switch m.Verb {
	case monolink.VerbOK, monolink.VerbErr, monolink.VerbPong, monolink.VerbPub, monolink.VerbReg, monolink.VerbFire, monolink.VerbPing:
		return true
	case monolink.VerbGet, monolink.VerbSet:
		switch m.Noun {
		case monolink.VerbReg, "JOBS.COUNT", "NEXT", "UPTIME", "VERSION":
			return true
		}
	}
	return false
}

func (a *Achtung) reply(m monolink.Message, verb, noun string, args ...string) {
	err := a.client.SendMessage(monolink.Message{Version: monolink.V2, ID: m.ID, From: a.client.Address(),
		To: m.From, Verb: verb, Noun: noun, Args: args})
	if err != nil {
		log.Warn("REPLY NOT SENT", "to", m.From, "noun", noun, "err", err)
	}
}

// fail answers err: achtung's own refusal as it is, and a failed save as
// no more than that — where the file is stays in the log.
func (a *Achtung) fail(m monolink.Message, err error) {
	var re *monolink.ReplyError
	switch {
	case errors.As(err, &re) && re.Detail != "":
		a.reply(m, monolink.VerbErr, re.Code, re.Detail)
	case errors.As(err, &re):
		a.reply(m, monolink.VerbErr, re.Code)
	case errors.Is(err, errNoJob):
		a.reply(m, monolink.VerbErr, monolink.CodeNAC)
	case errors.Is(err, errWrongKind):
		a.reply(m, monolink.VerbErr, monolink.CodeNAC, err.Error())
	case errors.Is(err, errInvalid):
		a.reply(m, monolink.VerbErr, monolink.CodeArg, err.Error())
	case errors.Is(err, errFull), errors.Is(err, errClosed):
		a.reply(m, monolink.VerbErr, monolink.CodeBusy, err.Error())
	default:
		log.Error("SAVE FAILED", "verb", m.Verb, "noun", m.Noun, "err", err)
		a.reply(m, monolink.VerbErr, monolink.CodeState, "could not save")
	}
}

// jobFrom reads NEW:<kind>'s arguments into a job, as of now:
//
//	TIMER <name> <duration>    a Go duration, a second to a year
//	ALARM <name> <date> <time> YYYY.MM.DD and H.M, local, after now, within ten years
//	EVERY <name> <duration>    a minute to a year
//	DAILY <name> <time>        H.M (or H:M), local
//
// Nothing may follow a date or a time: "07.30junk" is no half past seven.
func jobFrom(noun string, args []string, now time.Time) (Job, error) {
	kind, ok := ParseJobKind(noun)
	if !ok {
		return Job{}, monolink.Fail(monolink.CodeNoun, "achtung makes TIMER, ALARM, EVERY and DAILY")
	}
	want := 2
	if kind == KindAlarm {
		want = 3
	}
	if len(args) != want {
		return Job{}, monolink.Fail(monolink.CodeArgc, fmt.Sprintf("NEW:%s takes %d arguments", noun, want))
	}
	j := Job{Name: strings.TrimSpace(args[0]), Kind: kind}
	if err := checkName(j.Name); err != nil {
		return Job{}, monolink.Fail(monolink.CodeArg, err.Error())
	}
	switch kind {
	case KindTimer, KindEvery:
		least := time.Second
		if kind == KindEvery {
			least = minInterval
		}
		d, err := time.ParseDuration(strings.TrimSpace(args[1]))
		if err != nil || d < least || d > maxAhead {
			return Job{}, monolink.Fail("DUR", fmt.Sprintf("%q: a duration from %v to %v", args[1], least, maxAhead))
		}
		j.Due = now.Add(d)
		if kind == KindEvery {
			j.Interval = d
		}
	case KindAlarm:
		at, err := parseTimeLocal(args[1], args[2])
		if err != nil {
			return Job{}, monolink.Fail("TIME", err.Error())
		}
		if !at.After(now) || at.After(now.Add(maxAlarmAhead)) {
			return Job{}, monolink.Fail("TIME", "an alarm is after now, and within ten years")
		}
		j.Due = at
	case KindDaily:
		h, mi, err := parseClockLocal(args[1])
		if err != nil {
			return Job{}, monolink.Fail("TIME", err.Error())
		}
		j.AtHour, j.AtMin = h, mi
		j.Due = nextDailyAfter(now, h, mi)
	}
	return j, nil
}

func (a *Achtung) cmdNew(m monolink.Message) (string, []string, error) {
	j, err := jobFrom(m.Noun, m.Args, time.Now())
	if err != nil {
		return "", nil, err
	}
	if err := a.sched.Add(j); err != nil {
		return "", nil, err
	}
	log.Info("NEW", "kind", j.Kind, "name", j.Name, "due", j.Due.Format(time.DateTime), "from", m.From)
	return m.Noun, []string{j.Name}, nil
}

// cmdStop removes a job of that kind and name, and silences the buzzer —
// also when there is no such job any more: a one-shot that fired is gone,
// and STOP is how its ringing is stopped.
func (a *Achtung) cmdStop(m monolink.Message) (string, []string, error) {
	kind, ok := ParseJobKind(m.Noun)
	if !ok {
		return "", nil, monolink.Fail(monolink.CodeNoun, "achtung stops TIMER, ALARM, EVERY and DAILY")
	}
	if len(m.Args) != 1 {
		return "", nil, monolink.Fail(monolink.CodeArgc, "STOP takes a name")
	}
	name := strings.TrimSpace(m.Args[0])
	switch err := a.sched.Stop(name, kind); {
	case err == nil:
		log.Info("STOP", "kind", kind, "name", name, "from", m.From)
	case errors.Is(err, errNoJob):
		log.Debug("STOP: no such job; silencing", "kind", kind, "from", m.From)
	default:
		return "", nil, err
	}
	if err := a.silence(); err != nil {
		log.Warn("BUZZER NOT TOLD OFF", "err", err)
		return "", nil, monolink.Fail(monolink.CodeState, "the buzzer was not told OFF; STOP again")
	}
	return m.Noun, []string{name}, nil
}

func (a *Achtung) cmdGet(m monolink.Message) (string, []string, error) {
	switch m.Noun {
	case "LIST":
		if len(m.Args) > 1 {
			return "", nil, monolink.Fail(monolink.CodeArgc, "LIST takes nothing, or the name the last page ended at")
		}
		after := ""
		if len(m.Args) == 1 {
			after = m.Args[0]
		}
		return "LIST", a.listAfter(after), nil

	case "JOB":
		if len(m.Args) != 1 {
			return "", nil, monolink.Fail(monolink.CodeArgc, "JOB takes a name")
		}
		j, ok := a.sched.Get(strings.TrimSpace(m.Args[0]))
		if !ok {
			return "", nil, errNoJob
		}
		rem := max(time.Until(j.Due).Truncate(time.Second), 0)
		return "JOB", []string{j.Kind.String(), j.Name, rem.String(), serializeTimeLocal(j.Due)}, nil
	}
	return "", nil, monolink.Fail(monolink.CodeNoun, "")
}

// listAfter is GET:LIST's answer: the jobs named after `after`, by name, a
// kind and a name each, as many as one frame holds. A panel asks again
// after the last name it got, until an answer comes back empty.
func (a *Achtung) listAfter(after string) []string {
	jobs := a.sched.List()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
	var out []string
	for _, j := range jobs {
		if j.Name > after && len(out) < 2*listPage {
			out = append(out, j.Kind.String(), j.Name)
		}
	}
	return out
}

func (a *Achtung) eventLoop() {
	defer a.wg.Done()
	for ev := range a.sched.Events() {
		a.deliver(ev)
	}
}

// deliver tells the bus a job fired, and the buzzer to sound — again and
// again while the hub is out of reach, as when achtung starts before it
// has connected, for up to lateFire, and not past Shutdown.
func (a *Achtung) deliver(ev Event) {
	kind := ev.Job.Kind.String()
	log.Info("FIRE", "kind", kind, "name", ev.Job.Name)
	for told := false; ; {
		if !told {
			told = a.client.Send("ALL", monolink.VerbFire, kind, ev.Job.Name) == nil
		}
		if told && a.buzz(ev) == nil {
			return
		}
		if time.Since(ev.FiredAt) >= lateFire {
			log.Warn("FIRE MISSED: the bus was out of reach", "kind", kind, "name", ev.Job.Name)
			return
		}
		select {
		case <-a.stop:
			log.Warn("FIRE NOT DELIVERED: shutting down", "kind", kind, "name", ev.Job.Name)
			return
		case <-time.After(retryEvery):
		}
	}
}

// buzz sounds the buzzer for ev — unless it was silenced after ev fired: a
// STOP must not be undone by an alarm already on its way.
func (a *Achtung) buzz(ev Event) error {
	a.buzzMu.Lock()
	defer a.buzzMu.Unlock()
	if !ev.FiredAt.After(a.silenced) {
		return nil
	}
	return a.client.Send("VERTEX", monolink.VerbSet, "BUZZ.STATE", "ON")
}

// silence turns the buzzer off, and keeps every job fired until now from
// turning it on again.
func (a *Achtung) silence() error {
	a.buzzMu.Lock()
	defer a.buzzMu.Unlock()
	a.silenced = time.Now()
	return a.client.Send("VERTEX", monolink.VerbSet, "BUZZ.STATE", "OFF")
}

// parseClockLocal reads a clock time, H.M — or H:M, as typed by hand — and
// nothing after it.
func parseClockLocal(s string) (hour, minute int, err error) {
	t, err := time.Parse("15.4", strings.ReplaceAll(strings.TrimSpace(s), ":", "."))
	if err != nil {
		return 0, 0, fmt.Errorf("time %q: want H.M", s)
	}
	return t.Hour(), t.Minute(), nil
}

// parseTimeLocal reads a date, YYYY.MM.DD, and a clock time, H.M, as local
// time. A date that does not exist is refused, not rolled over; a time the
// clocks skip that day comes the gap later.
func parseTimeLocal(d, t string) (time.Time, error) {
	day, err := time.ParseInLocation("2006.1.2", strings.TrimSpace(d), time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("date %q: want YYYY.MM.DD", d)
	}
	h, mi, err := parseClockLocal(t)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(day.Year(), day.Month(), day.Day(), h, mi, 0, 0, time.Local), nil
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
