package image

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
)

// compressionQueue hands out the instance's compression slots.
//
// The slots are shared by every account, so the order they are granted in is
// a fairness decision, not a detail: in arrival order, one account that
// claims every image it holds keeps every other account's job waiting behind
// all of its own. A freed slot goes to the waiting job whose account runs the
// fewest jobs right now, and among those to the account that was granted a
// slot least recently; within one account jobs keep their arrival order. An
// account therefore runs a second job at once only when no other account is
// waiting.
//
// Waiting is cancellable: a job removed from the queue never held a slot, and
// one granted at the same moment it was cancelled gives the slot straight
// back.
type compressionQueue struct {
	mu      sync.Mutex
	slots   int
	total   int
	running map[uuid.UUID]int
	// lastGrant orders accounts by when they last received a slot; a smaller
	// value has waited longer. One entry per account that ever compressed.
	lastGrant map[uuid.UUID]uint64
	seq       uint64
	waiting   []*queuedCompression
}

type queuedCompression struct {
	accountID uuid.UUID
	ready     chan struct{}
	granted   bool
}

func newCompressionQueue(slots int) *compressionQueue {
	if slots < 1 {
		slots = 1
	}
	return &compressionQueue{
		slots:     slots,
		running:   make(map[uuid.UUID]int),
		lastGrant: make(map[uuid.UUID]uint64),
	}
}

// acquire waits for a slot for accountID, or for ctx to end. The returned
// release gives the slot back and must be called exactly once.
func (q *compressionQueue) acquire(ctx context.Context, accountID uuid.UUID) (func(), error) {
	w := &queuedCompression{accountID: accountID, ready: make(chan struct{})}
	q.mu.Lock()
	q.waiting = append(q.waiting, w)
	q.dispatchLocked()
	q.mu.Unlock()

	release := func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		q.releaseLocked(accountID)
	}
	select {
	case <-w.ready:
		return release, nil
	case <-ctx.Done():
		q.mu.Lock()
		defer q.mu.Unlock()
		if w.granted {
			q.releaseLocked(accountID)
		} else {
			for i, other := range q.waiting {
				if other == w {
					q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
					break
				}
			}
		}
		return nil, ctx.Err()
	}
}

func (q *compressionQueue) releaseLocked(accountID uuid.UUID) {
	q.total--
	if q.running[accountID] <= 1 {
		delete(q.running, accountID)
	} else {
		q.running[accountID]--
	}
	q.dispatchLocked()
}

func (q *compressionQueue) dispatchLocked() {
	for q.total < q.slots && len(q.waiting) > 0 {
		pick := 0
		for i := 1; i < len(q.waiting); i++ {
			a, b := q.waiting[i].accountID, q.waiting[pick].accountID
			if q.running[a] < q.running[b] ||
				(q.running[a] == q.running[b] && q.lastGrant[a] < q.lastGrant[b]) {
				pick = i
			}
		}
		w := q.waiting[pick]
		q.waiting = append(q.waiting[:pick], q.waiting[pick+1:]...)
		q.seq++
		q.lastGrant[w.accountID] = q.seq
		q.running[w.accountID]++
		q.total++
		w.granted = true
		close(w.ready)
	}
}

// compressionJob is a running or queued job as Delete sees it: something to
// stop and something to wait for.
type compressionJob struct {
	accountID uuid.UUID
	cancel    context.CancelFunc
	done      chan struct{}
}

// stopCompressionTimeout bounds how long Delete waits for a stopped job to
// settle. Stopping kills the codec processes and deletes a partial object,
// which takes seconds; past this the delete answers as it did before — the
// record is still held — rather than hanging the request.
const stopCompressionTimeout = 30 * time.Second

// registerCompressionJob records a job so Delete can stop it. A second job for
// the same record cannot exist (the claim is exclusive), but if one did the
// newer registration wins and the older one's finish leaves it alone.
func (s *Service) registerCompressionJob(accountID, imageID uuid.UUID, cancel context.CancelFunc) *compressionJob {
	job := &compressionJob{accountID: accountID, cancel: cancel, done: make(chan struct{})}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.jobs == nil {
		s.jobs = make(map[uuid.UUID]*compressionJob)
	}
	s.jobs[imageID] = job
	return job
}

// finishCompressionJob removes the job and wakes anyone waiting for it. Runs
// after the job has written its final status, so a waiter that wakes reads
// the settled record.
func (s *Service) finishCompressionJob(imageID uuid.UUID, job *compressionJob) {
	s.jobsMu.Lock()
	if cur, ok := s.jobs[imageID]; ok && cur == job {
		delete(s.jobs, imageID)
	}
	s.jobsMu.Unlock()
	job.cancel()
	close(job.done)
}

// stopCompression cancels this process's job for the record, queued or
// running, and waits for it to settle. It reports whether a job was stopped
// and has settled; false means there was none here, or it did not settle in
// time.
func (s *Service) stopCompression(ctx context.Context, accountID, imageID uuid.UUID) bool {
	s.jobsMu.Lock()
	job, ok := s.jobs[imageID]
	s.jobsMu.Unlock()
	if !ok || job.accountID != accountID {
		return false
	}
	job.cancel()
	timer := time.NewTimer(stopCompressionTimeout)
	defer timer.Stop()
	select {
	case <-job.done:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

// Progress pacing. A database write and a WebSocket frame to every viewer of
// the account go out per update, so the rate is bounded in time as well as in
// steps: a small source can be read in a fraction of a second.
const (
	progressStep        = 2
	progressMinInterval = 500 * time.Millisecond
	// A raw source is fed to the encoder at the rate it encodes, so the read
	// position is the job's position; 99 is held for the encoder's last
	// blocks and 100 arrives with the swap.
	maxLiveProgressRaw = 99
	// A transcode's source says little about the work left: a highly
	// compressed source is read in seconds and expands for minutes. Its
	// progress is the expanded byte count against an estimate, never above
	// the source read fraction, and it stops short of claiming completion
	// because the estimate can be wrong.
	maxLiveProgressTranscode = 90
	// transcodeExpansionEstimate is the expansion assumed for an OS image
	// archive (a typical .xz or .zst of one expands three to six times), with
	// a floor so a tiny source does not reach its estimate within a second.
	transcodeExpansionEstimate = 5
	transcodeEstimateFloor     = int64(1) << 30
)

// compressionProgress turns the bytes a job has moved into the percentage the
// record shows and decides when an update is worth sending. Both counters are
// fed from different goroutines (the decoder reads the source, the encoder
// reads the expanded stream), hence the mutex.
type compressionProgress struct {
	mu          sync.Mutex
	sourceTotal int64
	sourceRead  int64
	transcode   bool
	expanded    int64
	reported    int
	lastEmit    time.Time
	now         func() time.Time
	emit        func(percent int)
}

func newCompressionProgress(sourceTotal int64, transcode bool, emit func(int)) *compressionProgress {
	return &compressionProgress{sourceTotal: sourceTotal, transcode: transcode, emit: emit, now: time.Now}
}

func (p *compressionProgress) addSource(n int)   { p.add(int64(n), 0) }
func (p *compressionProgress) addExpanded(n int) { p.add(0, int64(n)) }

func (p *compressionProgress) add(source, expanded int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sourceRead += source
	p.expanded += expanded
	percent := p.percentLocked()
	if percent < p.reported+progressStep {
		return
	}
	now := p.now()
	if !p.lastEmit.IsZero() && now.Sub(p.lastEmit) < progressMinInterval {
		return
	}
	p.reported = percent
	p.lastEmit = now
	if p.emit != nil {
		p.emit(percent)
	}
}

func (p *compressionProgress) percentLocked() int {
	if p.sourceTotal <= 0 {
		return 0
	}
	percent := int(float64(p.sourceRead) / float64(p.sourceTotal) * 100)
	limit := maxLiveProgressRaw
	if p.transcode {
		limit = maxLiveProgressTranscode
		estimate := p.sourceTotal * transcodeExpansionEstimate
		if estimate < transcodeEstimateFloor {
			estimate = transcodeEstimateFloor
		}
		if byOutput := int(float64(p.expanded) / float64(estimate) * 100); byOutput < percent {
			percent = byOutput
		}
	}
	if percent > limit {
		percent = limit
	}
	return percent
}

// progressCounter reports every read through it to a callback.
type progressCounter struct {
	reader io.Reader
	add    func(n int)
}

func (c *progressCounter) Read(b []byte) (int, error) {
	n, err := c.reader.Read(b)
	if n > 0 {
		c.add(n)
	}
	return n, err
}
