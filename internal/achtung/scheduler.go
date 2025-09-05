package achtung

import (
	"container/heap"
	"time"
)

// Event is emitted when a job fires.
type Event struct {
	Job     Job
	FiredAt time.Time
}

// Public scheduler API (request types).
type addReq struct {
	job  Job
	resp chan error
}
type delReq struct {
	name string
	resp chan bool
}
type getReq struct {
	name string
	resp chan (struct {
		job Job
		ok  bool
	})
}
type listReq struct {
	resp chan []Job
}
type pauseReq struct {
	name string
	resp chan bool
}
type resumeReq struct {
	name string
	resp chan bool
}
type shutdownReq struct {
	done chan struct{}
}

// Scheduler owns all timing logic.
type Scheduler struct {
	events chan Event

	// requests:
	add      chan addReq
	del      chan delReq
	get      chan getReq
	list     chan listReq
	pause    chan pauseReq
	resume   chan resumeReq
	shutdown chan shutdownReq

	// internal state (owned by loop goroutine):
	h    jheap
	idx  map[string]*jitem // name -> heap item
	t    *time.Timer
	next time.Time
}

func NewScheduler() *Scheduler {
	s := &Scheduler{
		events:   make(chan Event, 64),
		add:      make(chan addReq),
		del:      make(chan delReq),
		get:      make(chan getReq),
		list:     make(chan listReq),
		pause:    make(chan pauseReq),
		resume:   make(chan resumeReq),
		shutdown: make(chan shutdownReq),
		idx:      make(map[string]*jitem),
	}
	heap.Init(&s.h)
	go s.loop()
	return s
}

func (s *Scheduler) Events() <-chan Event { return s.events }

func (s *Scheduler) Add(job Job) error {
	r := addReq{job: job, resp: make(chan error, 1)}
	s.add <- r
	return <-r.resp
}

func (s *Scheduler) Delete(name string) bool {
	r := delReq{name: name, resp: make(chan bool, 1)}
	s.del <- r
	return <-r.resp
}
func (s *Scheduler) Get(name string) (Job, bool) {
	r := getReq{name: name, resp: make(chan struct {
		job Job
		ok  bool
	}, 1)}
	s.get <- r
	x := <-r.resp
	return x.job, x.ok
}
func (s *Scheduler) List() []Job {
	r := listReq{resp: make(chan []Job, 1)}
	s.list <- r
	return <-r.resp
}
func (s *Scheduler) Pause(name string) bool {
	r := pauseReq{name: name, resp: make(chan bool, 1)}
	s.pause <- r
	return <-r.resp
}
func (s *Scheduler) Resume(name string) bool {
	r := resumeReq{name: name, resp: make(chan bool, 1)}
	s.resume <- r
	return <-r.resp
}
func (s *Scheduler) Shutdown() {
	r := shutdownReq{done: make(chan struct{})}
	s.shutdown <- r
	<-r.done
}

// ----- heap internals -----

type jitem struct {
	job Job
	idx int
}
type jheap []*jitem

func (h jheap) Len() int            { return len(h) }
func (h jheap) Less(i, j int) bool  { return h[i].job.Due.Before(h[j].job.Due) }
func (h jheap) Swap(i, j int)       { h[i], h[j] = h[j], h[i]; h[i].idx = i; h[j].idx = j }
func (h *jheap) Push(x interface{}) { *h = append(*h, x.(*jitem)) }
func (h *jheap) Pop() interface{} {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	it.idx = -1
	return it
}

func (s *Scheduler) loop() {
	for {
		var timerC <-chan time.Time
		if s.t != nil {
			timerC = s.t.C
		}

		select {
		case r := <-s.add:
			err := s.addJob(r.job)
			r.resp <- err

		case r := <-s.del:
			ok := s.deleteJob(r.name)
			r.resp <- ok

		case r := <-s.get:
			if it, ok := s.idx[r.name]; ok {
				r.resp <- struct {
					job Job
					ok  bool
				}{job: it.job, ok: true}
			} else {
				r.resp <- struct {
					job Job
					ok  bool
				}{ok: false}
			}

		case r := <-s.list:
			out := make([]Job, 0, len(s.idx))
			for _, it := range s.idx {
				out = append(out, it.job)
			}
			r.resp <- out

		case r := <-s.pause:
			ok := s.pauseJob(r.name)
			r.resp <- ok

		case r := <-s.resume:
			ok := s.resumeJob(r.name)
			r.resp <- ok

		case <-timerC:
			s.onTick()

		case r := <-s.shutdown:
			if s.t != nil {
				s.t.Stop()
			}
			close(s.events)
			r.done <- struct{}{}
			return
		}

		s.armTimer()
	}
}

func (s *Scheduler) addJob(j Job) error {
	// overwrite existing
	if old, ok := s.idx[j.Name]; ok {
		old.job.Active = false
		// leave old in heap; it’s marked inactive and will be skipped on pop
		delete(s.idx, j.Name)
	}
	j.Active = true
	it := &jitem{job: j}
	heap.Push(&s.h, it)
	s.idx[j.Name] = it
	return nil
}

func (s *Scheduler) deleteJob(name string) bool {
	it, ok := s.idx[name]
	if !ok {
		return false
	}
	it.job.Active = false
	delete(s.idx, name)
	return true
}

func (s *Scheduler) pauseJob(name string) bool {
	it, ok := s.idx[name]
	if !ok || it.job.Paused {
		return false
	}
	it.job.Paused = true
	return true
}

func (s *Scheduler) resumeJob(name string) bool {
	it, ok := s.idx[name]
	if !ok || !it.job.Paused {
		return false
	}
	it.job.Paused = false
	// If Due is in the past, fire ASAP (next tick will handle).
	return true
}

func (s *Scheduler) armTimer() {
	// Stop previous timer
	if s.t != nil {
		s.t.Stop()
		s.t = nil
	}
	// Peek next active, unpaused item
	//now := time.Now()
	for s.h.Len() > 0 {
		top := s.h[0]
		if !top.job.Active || top.job.Paused {
			heap.Pop(&s.h) // drop inactive/paused placeholders
			continue
		}
		// If due time is past, trigger immediately via short timer
		d := time.Until(top.job.Due)
		if d <= 0 {
			d = 0
		}
		s.next = top.job.Due
		s.t = time.NewTimer(d)
		return
	}
	// nothing to arm
	s.next = time.Time{}
}

func (s *Scheduler) onTick() {
	now := time.Now()
	for s.h.Len() > 0 {
		top := s.h[0]
		// skip inactive / paused
		if !top.job.Active || top.job.Paused {
			heap.Pop(&s.h)
			continue
		}
		if top.job.Due.After(now) {
			break
		}
		heap.Pop(&s.h)
		j := top.job // copy

		// If still active, emit event and reschedule if repeating
		if j.Active && !j.Paused {
			select {
			case s.events <- Event{Job: j, FiredAt: now}:
			default:
				// if consumer is slow, don't block; you can choose to block instead
			}
		}

		// repeating?
		if j.Kind == KindEvery && j.Active {
			j.Due = j.Due.Add(j.Interval)
			top.job = j
			heap.Push(&s.h, top)
			s.idx[j.Name] = top
		} else {
			// one-shot: mark inactive and drop from idx
			top.job.Active = false
			delete(s.idx, j.Name)
		}
	}
}
