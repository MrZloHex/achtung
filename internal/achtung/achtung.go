package achtung

import (
	"achtung/pkg/proto"
	"fmt"
	log "log/slog"
	"time"
)

type Achtung struct {
	client   *proto.Client
	sched    *Scheduler
	bootedAt time.Time
}

func NewAchtung(client *proto.Client) *Achtung {
	a := &Achtung{
		client:   client,
		sched:    NewScheduler(),
		bootedAt: time.Now(),
	}
	go a.eventLoop()
	return a
}

func (a *Achtung) Shutdown() { a.sched.Shutdown() }

// Cmd dispatches an incoming request by verb.
//
//	PING PING               -> PONG PONG
//	NEW  TIMER <name> <dur> -> OK TIMER <name>
//	NEW  ALARM <name> <d> <t> -> OK ALARM <name>
//	STOP TIMER|ALARM <name> -> OK <kind> <name>
//	GET  LIST               -> OK LIST [<kind> <name>...]
//	GET  JOB <name>         -> OK JOB <kind> <name> <rem> <due>
//	GET  UPTIME             -> OK UPTIME <dur>
func (a *Achtung) Cmd(req *proto.Request) {
	msg := req.Msg
	log.Debug("CMD", "from", msg.From, "verb", msg.Verb, "noun", msg.Noun, "args", msg.Args)

	switch msg.Verb {
	case "OK", "ERR", "PONG":
		log.Debug("IGNORE", "verb", msg.Verb, "noun", msg.Noun, "from", msg.From)
		return
	case "PING":
		req.Reply("PONG", "PONG")
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

func (a *Achtung) cmdNew(req *proto.Request) {
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

	default:
		log.Warn("UNKNOWN NOUN", "noun", msg.Noun, "from", msg.From)
		req.Reply("ERR", "NOUN")
	}
}

func (a *Achtung) cmdStop(req *proto.Request) {
	msg := req.Msg
	switch msg.Noun {
	case "TIMER", "ALARM":
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

func (a *Achtung) cmdGet(req *proto.Request) {
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

func kindStr(k JobKind) string {
	switch k {
	case KindTimer:
		return "TIMER"
	case KindAlarm:
		return "ALARM"
	case KindEvery:
		return "EVERY"
	default:
		return "UNK"
	}
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

func serializeTimeLocal(t time.Time) string {
	return fmt.Sprintf("%d.%d.%d:%d.%d", t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute())
}
