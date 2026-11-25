package credlock

import (
	"sync"
	"testing"

	"github.com/google/uuid"
)

// The lock's whole job: two credential-removal paths for the SAME
// user must not run their count-and-remove concurrently. Modeled as two
// goroutines each reading a shared counter, checking it is >1, then
// decrementing — the check-then-act the guards perform. Without the lock they
// interleave and both remove; with it, one is refused.
func TestWithUserSerializesSameUser(t *testing.T) {
	l := New()
	userID := uuid.New()

	remaining := 2 // password + one passkey, say
	removed := 0
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup

	removeOne := func() {
		defer done.Done()
		start.Wait()
		_ = l.WithUser(userID, func() error {
			if remaining <= 1 {
				return nil // guard refuses: this is the last credential
			}
			remaining--
			removed++
			return nil
		})
	}

	done.Add(2)
	go removeOne()
	go removeOne()
	start.Done()
	done.Wait()

	if removed != 1 {
		t.Fatalf("both removals passed the guard: removed=%d, remaining=%d — the lockout race is open", removed, remaining)
	}
	if remaining != 1 {
		t.Fatalf("user must keep exactly one sign-in method, remaining=%d", remaining)
	}
}

// Different users must not block each other — the lock is per user, not global.
func TestWithUserDoesNotSerializeDifferentUsers(t *testing.T) {
	l := New()
	a, b := uuid.New(), uuid.New()

	// If b's lock waited on a's, this would deadlock: a holds its lock and
	// blocks until b's callback runs, b's callback runs only once it holds b's
	// lock. Distinct locks let both proceed.
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = l.WithUser(a, func() error {
			<-release
			return nil
		})
	}()
	_ = l.WithUser(b, func() error {
		close(release)
		return nil
	})
	wg.Wait()
}
