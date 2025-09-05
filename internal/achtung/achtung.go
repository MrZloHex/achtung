package achtung

import (
	"achtung/pkg/protocol"
	"fmt"
	log "log/slog"
	"strconv"
	"strings"
	"time"
)

type Achtung struct {
	ptcl  *protocol.Protocol
	sched *Scheduler
}

func NewAchtung(ptcl *protocol.Protocol) *Achtung {
	a := &Achtung{
		ptcl:  ptcl,
		sched: NewScheduler(),
	}
	go a.eventLoop()
	return a
}

func (a *Achtung) Shutdown() { a.sched.Shutdown() }

// Cmd receives tokens after "ACHTUNG:".
// Examples:
//
//	SET TIMER WASHING_MACHINE 30m some other stuff
//	SET ALARM coffee 2025-09-05T21:00:00+03:00 bring:milk
//	SET EVERY stretch 45m
//	GET LIST
//	GET TIMER WASHING_MACHINE
//	DELETE TIMER WASHING_MACHINE
//	PAUSE TIMER WASHING_MACHINE
//	RESUME TIMER WASHING_MACHINE
func (a *Achtung) Cmd(from string, args []string) {
	if len(args) == 0 {
		a.ptcl.Transmit(from, "ERR:ARGS")
		return
	}
	switch args[0] {
	case "SET":
		a.cmdSet(from, args[1:])
	case "GET":
		a.cmdGet(from, args[1:])
	case "DELETE":
		a.cmdDelete(from, args[1:])
	case "PAUSE":
		a.cmdPause(from, args[1:])
	case "RESUME":
		a.cmdResume(from, args[1:])
	default:
		a.ptcl.Transmit(from, "VERB:UNK")
	}
}

func (a *Achtung) cmdSet(from string, args []string) {
	if len(args) < 1 {
		a.ptcl.Transmit(from, "ARGC:LESS")
		return
	}
	switch args[0] {
	case "TIMER":
		if len(args) < 3 {
			a.ptcl.Transmit(from, "ARGC:LESS")
			return
		}
		name := args[1]
		d, err := time.ParseDuration(args[2])
		if err != nil {
			a.ptcl.Transmit(from, "DUR:BAD", args[2])
			return
		}
		data := args[3:]
		job := Job{
			Name: name, From: from, Kind: KindTimer,
			Due: time.Now().Add(d), Data: data,
		}
		if err := a.sched.Add(job); err != nil {
			a.ptcl.Transmit(from, "SET:ERR", err.Error())
			return
		}
		a.ptcl.Transmit(from, "SET:OK:TIMER", name, d.String())

	case "ALARM":
		if len(args) < 3 {
			a.ptcl.Transmit(from, "ARGC:LESS")
			return
		}
		name := args[1]
		tm, err := parseTimeLocal(args[2], args[3])
		if err != nil {
			a.ptcl.Transmit(from, "TIME:BAD", args[2], args[3])
			return
		}
		data := args[4:]
		job := Job{
			Name: name, From: from, Kind: KindAlarm,
			Due: tm, Data: data,
		}
		if err := a.sched.Add(job); err != nil {
			a.ptcl.Transmit(from, "SET:ERR", err.Error())
			return
		}
		a.ptcl.Transmit(from, "SET:OK:ALARM", name, tm.Format(time.RFC3339))

	case "EVERY":
		if len(args) < 3 {
			a.ptcl.Transmit(from, "ARGC:LESS")
			return
		}
		name := args[1]
		intv, err := time.ParseDuration(args[2])
		if err != nil || intv <= 0 {
			a.ptcl.Transmit(from, "INTV:BAD", args[2])
			return
		}
		data := args[3:]
		job := Job{
			Name: name, From: from, Kind: KindEvery,
			Due: time.Now().Add(intv), Interval: intv, Data: data,
		}
		if err := a.sched.Add(job); err != nil {
			a.ptcl.Transmit(from, "SET:ERR", err.Error())
			return
		}
		a.ptcl.Transmit(from, "SET:OK:EVERY", name, intv.String())

	default:
		a.ptcl.Transmit(from, "NOUN:UNK")
	}
}

func (a *Achtung) cmdGet(from string, args []string) {
	if len(args) < 1 {
		a.ptcl.Transmit(from, "ARGC:LESS")
		return
	}
	switch strings.ToUpper(args[0]) {
	case "LIST":
		jobs := a.sched.List()
		a.ptcl.Transmit(from, "TIMER:LIST", strconv.Itoa(len(jobs)))
		now := time.Now()
		for _, j := range jobs {
			if !j.Active {
				continue
			}
			kind := kindStr(j.Kind)
			rem := j.Due.Sub(now).Truncate(time.Second)
			if rem < 0 {
				rem = 0
			}
			a.ptcl.Transmit(from, "TIMER:INFO", j.Name, kind, rem.String(), j.Due.Format(time.RFC3339), j.From)
		}
	case "TIMER":
		if len(args) < 2 {
			a.ptcl.Transmit(from, "ARGC:LESS")
			return
		}
		name := args[1]
		j, ok := a.sched.Get(name)
		if !ok || !j.Active {
			a.ptcl.Transmit(from, "TIMER:NF", name)
			return
		}
		now := time.Now()
		rem := j.Due.Sub(now).Truncate(time.Second)
		if rem < 0 {
			rem = 0
		}
		a.ptcl.Transmit(from, "TIMER:LEFT", j.Name, kindStr(j.Kind), rem.String(), j.Due.Format(time.RFC3339))
	default:
		a.ptcl.Transmit(from, "NOUN:UNK")
	}
}

func (a *Achtung) cmdDelete(from string, args []string) {
	if len(args) < 2 || strings.ToUpper(args[0]) != "TIMER" {
		a.ptcl.Transmit(from, "ARGC:LESS")
		return
	}
	name := args[1]
	ok := a.sched.Delete(name)
	if !ok {
		a.ptcl.Transmit(from, "TIMER:NF", name)
		return
	}
	a.ptcl.Transmit(from, "DELETE:OK:TIMER", name)
}

func (a *Achtung) cmdPause(from string, args []string) {
	if len(args) < 2 || strings.ToUpper(args[0]) != "TIMER" {
		a.ptcl.Transmit(from, "ARGC:LESS")
		return
	}
	name := args[1]
	if a.sched.Pause(name) {
		a.ptcl.Transmit(from, "PAUSE:OK:TIMER", name)
	} else {
		a.ptcl.Transmit(from, "TIMER:NF", name)
	}
}

func (a *Achtung) cmdResume(from string, args []string) {
	if len(args) < 2 || strings.ToUpper(args[0]) != "TIMER" {
		a.ptcl.Transmit(from, "ARGC:LESS")
		return
	}
	name := args[1]
	if a.sched.Resume(name) {
		a.ptcl.Transmit(from, "RESUME:OK:TIMER", name)
	} else {
		a.ptcl.Transmit(from, "TIMER:NF", name)
	}
}

func (a *Achtung) eventLoop() {
	for ev := range a.sched.Events() {
		j := ev.Job
		payload := "CB:" + strings.ToUpper(kindStr(j.Kind)) + ":" + j.Name
		if len(j.Data) > 0 {
			payload += ":" + strings.Join(j.Data, ":")
		}
		log.Info("FIRE", "name", j.Name, "kind", kindStr(j.Kind), "from", j.From)
		a.ptcl.Transmit(j.From, payload)
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
		return time.Now(), err
	}
	_, err = fmt.Sscanf(t, "%d.%d", &hour, &minute)
	if err != nil {
		return time.Now(), err
	}

	return time.Date(year, time.Month(month), day, hour, minute, 0, 0, time.Local), nil
}
