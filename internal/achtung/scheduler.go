package achtung

import (
	"container/heap"
	"errors"
	"fmt"
	log "log/slog"
	"sync"
	"time"
)

var (
	errClosed    = errors.New("achtung is shutting down")
	errFull      = fmt.Errorf("as many jobs as achtung keeps (%d)", maxJobs)
	errNoJob     = errors.New("no such job")
	errWrongKind = errors.New("a job of another kind")
	errInvalid   = errors.New("not a job")
)

const (
	// maxPending is how many fired jobs may wait for the bus. Past it the
	// oldest goes, said loudly: a reader that far behind is a bus long out
	// of reach, and the fire has been missed anyway.
	maxPending = 1024
	// saveRetry is how soon a failed save of the scheduler's own changes —
	// jobs fired, repeating ones moved on — is tried again.
	saveRetry = 10 * time.Second
)

// Event is emitted when a job fires.
type Event struct {
	Job     Job
	FiredAt time.Time
}

type addReq struct {
	job  Job
	resp chan error
}
type stopReq struct {
	name string
	kind JobKind
	any  bool // whatever its kind
	resp chan error
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
	name   string
	paused bool
	resp   chan error
}
type shutdownReq struct {
	done chan struct{}
}

// Scheduler owns all timing logic, and the job set: one goroutine, so that
// every change is made, saved and answered in turn.
type Scheduler struct {
	events chan Event

	// snapshots carries the full job set after every change, for
	// announcing. Buffered depth 1 and coalescing: a consumer that falls
	// behind simply skips to the newest state, which is all it needs.
	snapshots chan []Job

	add      chan addReq
	stop     chan stopReq
	get      chan getReq
	list     chan listReq
	pause    chan pauseReq
	shutdown chan shutdownReq
	done     chan struct{} // closed once the loop has stopped
	once     sync.Once

	// persist saves the job set; nil keeps it in memory only.
	persist func([]Job) error

	// internal state (owned by loop goroutine):
	h       jheap
	idx     map[string]*jitem // name -> heap item; every item is in the heap
	t       *time.Timer
	pending []Event // fired, not yet taken by Events' reader
	dirty   bool    // the scheduler's own last change is not saved
	retry   *time.Timer
	max     int
}

// NewScheduler starts a scheduler. persist, if not nil, saves the job set:
// a change is saved before it is answered, and one whose save fails is
// undone.
func NewScheduler(persist func([]Job) error) *Scheduler {
	s := &Scheduler{
		events:    make(chan Event, 16),
		snapshots: make(chan []Job, 1),
		add:       make(chan addReq),
		stop:      make(chan stopReq),
		get:       make(chan getReq),
		list:      make(chan listReq),
		pause:     make(chan pauseReq),
		shutdown:  make(chan shutdownReq),
		done:      make(chan struct{}),
		persist:   persist,
		idx:       make(map[string]*jitem),
		max:       maxJobs,
	}
	heap.Init(&s.h)
	go s.loop()
	return s
}

// Events yields every job that fires, in the order they fired. None is
// dropped for a slow reader: they wait for it (maxPending).
func (s *Scheduler) Events() <-chan Event { return s.events }

// Snapshots yields the job set after each change. See the field comment
// for the coalescing behaviour.
func (s *Scheduler) Snapshots() <-chan []Job { return s.snapshots }

func (s *Scheduler) jobs() []Job {
	snap := make([]Job, 0, len(s.idx))
	for _, it := range s.idx {
		snap = append(snap, it.job)
	}
	return snap
}

// notifyChanged publishes the current job set. Only the loop goroutine
// calls this, so draining then sending cannot block: we are the sole
// sender and have just made room.
func (s *Scheduler) notifyChanged() {
	select {
	case <-s.snapshots:
	default:
	}
	s.snapshots <- s.jobs()
}

// commit saves the job set as it now is and, saved, publishes it. The
// caller undoes its change if it fails.
func (s *Scheduler) commit() error {
	if s.persist != nil {
		if err := s.persist(s.jobs()); err != nil {
			return err
		}
	}
	s.dirty = false
	s.notifyChanged()
	return nil
}

// Add arms job, replacing one of the same name. It is saved before Add
// returns; a job that could not be saved is not armed.
func (s *Scheduler) Add(job Job) error {
	r := addReq{job: job, resp: make(chan error, 1)}
	select {
	case s.add <- r:
		return <-r.resp
	case <-s.done:
		return errClosed
	}
}

// Stop removes the job name, which must be of kind.
func (s *Scheduler) Stop(name string, kind JobKind) error {
	return s.stopReq(stopReq{name: name, kind: kind})
}

// Delete removes the job name, whatever its kind, and says whether there
// was one to remove.
func (s *Scheduler) Delete(name string) bool {
	return s.stopReq(stopReq{name: name, any: true}) == nil
}

func (s *Scheduler) stopReq(r stopReq) error {
	r.resp = make(chan error, 1)
	select {
	case s.stop <- r:
		return <-r.resp
	case <-s.done:
		return errClosed
	}
}

func (s *Scheduler) Get(name string) (Job, bool) {
	r := getReq{name: name, resp: make(chan struct {
		job Job
		ok  bool
	}, 1)}
	select {
	case s.get <- r:
	case <-s.done:
		return Job{}, false
	}
	x := <-r.resp
	return x.job, x.ok
}

func (s *Scheduler) List() []Job {
	r := listReq{resp: make(chan []Job, 1)}
	select {
	case s.list <- r:
		return <-r.resp
	case <-s.done:
		return nil
	}
}

func (s *Scheduler) Pause(name string) bool  { return s.setPaused(name, true) }
func (s *Scheduler) Resume(name string) bool { return s.setPaused(name, false) }

func (s *Scheduler) setPaused(name string, paused bool) bool {
	r := pauseReq{name: name, paused: paused, resp: make(chan error, 1)}
	select {
	case s.pause <- r:
		return <-r.resp == nil
	case <-s.done:
		return false
	}
}

// Shutdown stops the scheduler: nothing fires, and every call after
// returns at once. Twice is harmless.
func (s *Scheduler) Shutdown() {
	s.once.Do(func() {
		r := shutdownReq{done: make(chan struct{})}
		s.shutdown <- r
		<-r.done
	})
}

// ----- heap internals -----

type jitem struct {
	job Job
	idx int
}
type jheap []*jitem

func (h jheap) Len() int           { return len(h) }
func (h jheap) Less(i, j int) bool { return h[i].job.Due.Before(h[j].job.Due) }
func (h jheap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].idx = i; h[j].idx = j }
func (h *jheap) Push(x interface{}) {
	it := x.(*jitem)
	it.idx = len(*h)
	*h = append(*h, it)
}
func (h *jheap) Pop() interface{} {
	old := *h
	n := len(old)
	it := old[n-1]
	old[n-1] = nil // the backing array keeps no job alive
	*h = old[:n-1]
	it.idx = -1
	return it
}

func (s *Scheduler) insert(it *jitem) {
	heap.Push(&s.h, it)
	s.idx[it.job.Name] = it
}

// remove takes it out of the heap at once: a job replaced or stopped
// leaves nothing behind to be skipped later.
func (s *Scheduler) remove(it *jitem) {
	heap.Remove(&s.h, it.idx)
	delete(s.idx, it.job.Name)
}

func (s *Scheduler) loop() {
	for {
		var timerC, retryC <-chan time.Time
		if s.t != nil {
			timerC = s.t.C
		}
		if s.retry != nil {
			retryC = s.retry.C
		}
		var out chan<- Event
		var head Event
		if len(s.pending) > 0 {
			out, head = s.events, s.pending[0]
		}

		select {
		case r := <-s.add:
			r.resp <- s.addJob(r.job)

		case r := <-s.stop:
			r.resp <- s.stopJob(r)

		case r := <-s.get:
			it, ok := s.idx[r.name]
			var j Job
			if ok {
				j = it.job
			}
			r.resp <- struct {
				job Job
				ok  bool
			}{job: j, ok: ok}

		case r := <-s.list:
			r.resp <- s.jobs()

		case r := <-s.pause:
			r.resp <- s.pauseJob(r.name, r.paused)

		case out <- head:
			s.pending[0] = Event{}
			s.pending = s.pending[1:]

		case <-retryC:
			s.retry = nil
			s.commitOwn()

		case <-timerC:
			s.onTick()

		case r := <-s.shutdown:
			if s.t != nil {
				s.t.Stop()
			}
			if len(s.pending) > 0 {
				log.Warn("FIRED, NOT HANDED ON: shutting down", "count", len(s.pending))
			}
			close(s.done)
			close(s.events)
			close(s.snapshots)
			r.done <- struct{}{}
			return
		}

		s.armTimer()
	}
}

func (s *Scheduler) addJob(j Job) error {
	if err := j.valid(); err != nil {
		return fmt.Errorf("%w: %v", errInvalid, err)
	}
	old, replaced := s.idx[j.Name]
	if !replaced && len(s.idx) >= s.max {
		return errFull
	}
	if replaced {
		s.remove(old)
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
	s.insert(it)
	if err := s.commit(); err != nil {
		s.remove(it)
		if replaced {
			s.insert(old)
		}
		return err
	}
	return nil
}

func (s *Scheduler) stopJob(r stopReq) error {
	it, ok := s.idx[r.name]
	if !ok {
		return errNoJob
	}
	if !r.any && it.job.Kind != r.kind {
		return fmt.Errorf("%w: %s is a %s", errWrongKind, r.name, it.job.Kind)
	}
	s.remove(it)
	if err := s.commit(); err != nil {
		s.insert(it)
		return err
	}
	return nil
}

func (s *Scheduler) pauseJob(name string, paused bool) error {
	it, ok := s.idx[name]
	if !ok || it.job.Paused == paused {
		return errNoJob
	}
	it.job.Paused = paused
	if err := s.commit(); err != nil {
		it.job.Paused = !paused
		return err
	}
	return nil
}

// commitOwn saves what the scheduler changed by itself — jobs fired, and
// repeating ones moved on. There is nobody to undo it for: a failure is
// said loudly and tried again in saveRetry.
func (s *Scheduler) commitOwn() {
	if err := s.commit(); err != nil {
		log.Error("JOBS NOT SAVED: trying again", "in", saveRetry, "err", err)
		s.dirty = true
		s.notifyChanged()
		if s.retry == nil {
			s.retry = time.NewTimer(saveRetry)
		}
	}
}

func (s *Scheduler) armTimer() {
	if s.t != nil {
		s.t.Stop()
		s.t = nil
	}
	var paused []*jitem
	defer func() {
		for _, p := range paused {
			heap.Push(&s.h, p)
		}
	}()
	for s.h.Len() > 0 {
		top := s.h[0]
		if top.job.Paused {
			paused = append(paused, heap.Pop(&s.h).(*jitem))
			continue
		}
		s.t = time.NewTimer(max(time.Until(top.job.Due), 0))
		return
	}
}

// fire queues ev for Events' reader. The scheduler never waits for it.
func (s *Scheduler) fire(ev Event) {
	if len(s.pending) >= maxPending {
		log.Error("FIRE DROPPED: too many waiting for the bus", "kind", s.pending[0].Job.Kind, "name", s.pending[0].Job.Name)
		s.pending[0] = Event{}
		s.pending = s.pending[1:]
	}
	s.pending = append(s.pending, ev)
}

func (s *Scheduler) onTick() {
	now := time.Now()
	changed := false
	var paused []*jitem
	for s.h.Len() > 0 {
		top := s.h[0]
		if top.job.Paused {
			paused = append(paused, heap.Pop(&s.h).(*jitem))
			continue
		}
		if top.job.Due.After(now) {
			break
		}
		heap.Pop(&s.h)
		j := top.job
		s.fire(Event{Job: j, FiredAt: now})

		// Re-arm repeating jobs to their next occurrence strictly after
		// `now`. NextAfter jumps the whole gap in one step, so a job whose
		// Due went stale during an outage fires once here and then lands in
		// the future -- it does not spin, firing once per interval missed.
		if next, ok := j.NextAfter(now); ok {
			top.job.Due = next
			heap.Push(&s.h, top)
		} else {
			if j.Repeating() {
				log.Warn("JOB DROPPED: no next time", "kind", j.Kind, "name", j.Name)
			}
			delete(s.idx, j.Name)
		}
		changed = true
	}

	for _, p := range paused {
		heap.Push(&s.h, p)
	}

	if changed {
		s.commitOwn()
	}
}
