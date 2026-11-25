package image

import (
	"context"
	"sync"
)

// memoryBudget is the container-wide compression memory budget: a weighted
// semaphore in MiB over the per-process xz limit.
//
// MaxConcurrentCompressions counts JOBS, and the xz limit bounds one PROCESS,
// but a transcode is two processes (decoder and encoder, piped) — so the
// ceiling a container had to survive was jobs × 2 × limit with nothing
// enforcing it. A job takes its whole weight here BEFORE it starts, one or
// two process limits, atomically: taking it per process would let two
// transcodes each hold a decoder and wait forever for an encoder. Jobs
// beyond the budget wait, as they do for a slot; nothing is refused.
type memoryBudget struct {
	mu       sync.Mutex
	cond     *sync.Cond
	capacity int
	used     int
}

func newMemoryBudget(capacityMB int) *memoryBudget {
	b := &memoryBudget{capacity: capacityMB}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// acquire blocks until weight MiB fit, or the context ends. A weight above the
// whole capacity is admitted once the budget is empty rather than never —
// the configuration floor makes that a defensive branch, not a live path.
func (b *memoryBudget) acquire(ctx context.Context, weight int) error {
	if b == nil || weight <= 0 {
		return nil
	}
	stop := context.AfterFunc(ctx, func() {
		b.mu.Lock()
		b.cond.Broadcast()
		b.mu.Unlock()
	})
	defer stop()
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.used+weight > b.capacity && !(b.used == 0 && weight > b.capacity) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		b.cond.Wait()
	}
	b.used += weight
	return nil
}

func (b *memoryBudget) release(weight int) {
	if b == nil || weight <= 0 {
		return
	}
	b.mu.Lock()
	b.used -= weight
	if b.used < 0 {
		b.used = 0
	}
	b.cond.Broadcast()
	b.mu.Unlock()
}

// inUse reports the MiB currently held, for tests and the boot log.
func (b *memoryBudget) inUse() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}
