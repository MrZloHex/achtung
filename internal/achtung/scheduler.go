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

	// snapshots carries the full job set after every mutation, for
	// persistence. Buffered depth 1 and coalescing: a consumer that falls
	// behind simply skips to the newest state, which is all it needs.
	snapshots chan []Job

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
		events:    make(chan Event, 64),
		snapshots: make(chan []Job, 1),
		add:       make(chan addReq),
		del:       make(chan delReq),
		get:       make(chan getReq),
		list:      make(chan listReq),
		pause:     make(chan pauseReq),
		resume:    make(chan resumeReq),
		shutdown:  make(chan shutdownReq),
		idx:       make(map[string]*jitem),
	}
	heap.Init(&s.h)
	go s.loop()
	return s
}

func (s *Scheduler) Events() <-chan Event { return s.events }

// Snapshots yields the job set after each change. See the field comment
// for the coalescing behaviour.
func (s *Scheduler) Snapshots() <-chan []Job { return s.snapshots }

// notifyChanged publishes the current job set. Only the loop goroutine
// calls this, so draining then sending cannot block: we are the sole
// sender and have just made room.
func (s *Scheduler) notifyChanged() {
	snap := make([]Job, 0, len(s.idx))
	for _, it := range s.idx {
		snap = append(snap, it.job)
	}
	select {
	case <-s.snapshots:
	default:
	}
	s.snapshots <- snap
}

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
			if err == nil {
				s.notifyChanged()
			}
			r.resp <- err

		case r := <-s.del:
			ok := s.deleteJob(r.name)
			if ok {
				s.notifyChanged()
			}
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
			if ok {
				s.notifyChanged()
			}
			r.resp <- ok

		case r := <-s.resume:
			ok := s.resumeJob(r.name)
			if ok {
				s.notifyChanged()
			}
			r.resp <- ok

		case <-timerC:
			s.onTick()

		case r := <-s.shutdown:
			if s.t != nil {
				s.t.Stop()
			}
			close(s.events)
			close(s.snapshots)
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
	// A repeating job handed to us with a stale Due (from disk, or from a
	// caller that computed it loosely) is pulled forward before it enters
	// the heap, so it cannot fire immediately on add.
	if j.Repeating() {
		if next, ok := j.NextAfter(time.Now()); ok {
			j.Due = next
		}
	}
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
	if s.t != nil {
		s.t.Stop()
		s.t = nil
	}

	var paused []*jitem
	for s.h.Len() > 0 {
		top := s.h[0]
		if !top.job.Active {
			heap.Pop(&s.h)
			continue
		}
		if top.job.Paused {
			paused = append(paused, top)
			heap.Pop(&s.h)
			continue
		}
		d := time.Until(top.job.Due)
		if d <= 0 {
			d = 0
		}
		s.next = top.job.Due
		s.t = time.NewTimer(d)
		for _, p := range paused {
			heap.Push(&s.h, p)
		}
		return
	}

	for _, p := range paused {
		heap.Push(&s.h, p)
	}
	s.next = time.Time{}
}

func (s *Scheduler) onTick() {
	now := time.Now()
	fired := false
	var paused []*jitem
	for s.h.Len() > 0 {
		top := s.h[0]
		if !top.job.Active {
			heap.Pop(&s.h)
			continue
		}
		if top.job.Paused {
			paused = append(paused, top)
			heap.Pop(&s.h)
			continue
		}
		if top.job.Due.After(now) {
			break
		}
		heap.Pop(&s.h)
		j := top.job

		select {
		case s.events <- Event{Job: j, FiredAt: now}:
		default:
		}

		// Re-arm repeating jobs to their next occurrence strictly after
		// `now`. NextAfter jumps the whole gap in one step, so a job whose
		// Due went stale during an outage fires once here and then lands in
		// the future -- it does not spin, firing once per interval missed.
		if next, ok := j.NextAfter(now); ok {
			j.Due = next
			top.job = j
			heap.Push(&s.h, top)
			s.idx[j.Name] = top
		} else {
			top.job.Active = false
			delete(s.idx, j.Name)
		}
		fired = true
	}

	for _, p := range paused {
		heap.Push(&s.h, p)
	}

	if fired {
		s.notifyChanged()
	}
}
