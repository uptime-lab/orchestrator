package auth

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

// clockedThrottle is a throttle whose time the test moves.
func clockedThrottle() (*loginThrottle, *time.Time) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	t := newLoginThrottle()
	t.now = func() time.Time { return now }
	return t, &now
}

const (
	strangerIP = "203.0.113.7"
	ownerIP    = "198.51.100.20"
)

// Guesses spread over many source addresses were bounded by nothing: the
// per-IP limiter counts sources, not targets. After the free failures every
// further failure for the same address closes it for a doubling delay, and a
// refused attempt is answered before any password comparison.
func TestLoginIsThrottledPerAddressAfterRepeatedFailures(t *testing.T) {
	f := newLinkFixture(t, "owner@example.com", "correct horse battery")
	hasher := &countingHasher{PasswordHasher: f.svc.passwordHasher}
	f.svc.passwordHasher = hasher
	throttle, now := clockedThrottle()
	f.svc.loginThrottle = throttle

	for i := 0; i <= loginThrottleFreeFailures; i++ {
		if _, err := f.svc.Login("Owner@Example.com", "wrong guess", ownerIP); !errors.Is(err, app_errors.ErrInvalidCredentials) {
			t.Fatalf("failure %d: got %v, want ErrInvalidCredentials", i+1, err)
		}
	}
	compared := len(hasher.compared)

	// Closed now from this source — even to the right password, or it would
	// be an oracle.
	_, err := f.svc.Login("owner@example.com", "correct horse battery", ownerIP)
	var throttled *app_errors.LoginThrottledError
	if !errors.As(err, &throttled) {
		t.Fatalf("got %v, want LoginThrottledError", err)
	}
	if throttled.RetryAfter <= 0 || throttled.RetryAfter > loginThrottleBaseDelay {
		t.Fatalf("first delay %v, want (0, %v]", throttled.RetryAfter, loginThrottleBaseDelay)
	}
	if len(hasher.compared) != compared {
		t.Fatal("a throttled attempt still compared a password")
	}

	// Refused attempts do not extend the window.
	for i := 0; i < 20; i++ {
		_, _ = f.svc.Login("owner@example.com", "wrong guess", ownerIP)
	}
	*now = now.Add(loginThrottleBaseDelay)
	if _, err := f.svc.Login("owner@example.com", "correct horse battery", ownerIP); err != nil {
		t.Fatalf("after the delay the owner must get in, got %v", err)
	}
	// And the success clears the address.
	if wait := throttle.admit("owner@example.com", ownerIP); wait != 0 {
		t.Fatalf("a successful sign-in left a delay of %v", wait)
	}
}

// A stranger failing for someone else's address closes it only for the
// stranger's own source: the owner, from a source that has not failed, still
// reaches the password comparison and gets in.
func TestLoginThrottleDoesNotLockTheOwnerOutForAStranger(t *testing.T) {
	f := newLinkFixture(t, "owner@example.com", "correct horse battery")
	throttle, now := clockedThrottle()
	f.svc.loginThrottle = throttle

	// The stranger keeps the address at the cap for a while.
	for i := 0; i < loginThrottleFreeFailures+10; i++ {
		_, _ = f.svc.Login("owner@example.com", "wrong guess", strangerIP)
		*now = now.Add(time.Second)
	}
	if _, err := f.svc.Login("owner@example.com", "wrong guess", strangerIP); !errors.As(err, new(*app_errors.LoginThrottledError)) {
		t.Fatalf("the stranger's own source must be braked, got %v", err)
	}
	if _, err := f.svc.Login("owner@example.com", "correct horse battery", ownerIP); err != nil {
		t.Fatalf("the owner's correct password from another source was refused: %v", err)
	}
}

// Past the address-wide ceiling, sources that already failed for the address
// share one attempt per spacing interval, while a source that has not failed
// is never refused.
func TestLoginThrottleAddressCeilingSpacesFailedSourcesOnly(t *testing.T) {
	throttle, now := clockedThrottle()
	const email = "owner@example.com"
	for i := 0; i < loginThrottleAddressFreeFailures+1; i++ {
		if w := throttle.admit(email, fmt.Sprintf("192.0.2.%d", i)); w != 0 {
			t.Fatalf("source %d refused before the ceiling: %v", i, w)
		}
	}
	// A source that has failed once is now spaced.
	if w := throttle.admit(email, "192.0.2.0"); w <= 0 || w > loginThrottleAddressSpacing {
		t.Fatalf("a failed source past the ceiling got %v, want (0, %v]", w, loginThrottleAddressSpacing)
	}
	// A source that has not failed is admitted at once.
	if w := throttle.admit(email, ownerIP); w != 0 {
		t.Fatalf("a source with no failures was refused for %v", w)
	}
	*now = now.Add(loginThrottleAddressSpacing)
	if w := throttle.admit(email, "192.0.2.0"); w != 0 {
		t.Fatalf("after the spacing a failed source must get its slot, got %v", w)
	}
	if w := throttle.admit(email, "192.0.2.1"); w <= 0 {
		t.Fatal("two failed sources shared one slot")
	}
}

// gatedHasher holds every comparison until released, so a test can line up
// parallel attempts the way they arrive while the first bcrypt still runs.
type gatedHasher struct {
	services.PasswordHasher
	release  chan struct{}
	compared atomic.Int32
}

func (g *gatedHasher) ComparePasswords(hashedPassword, plainPassword string) error {
	g.compared.Add(1)
	<-g.release
	return g.PasswordHasher.ComparePasswords(hashedPassword, plainPassword)
}

// Attempts are counted when admitted, not when their comparison fails, so
// parallel guesses for one address cannot all pass the check while the first
// comparison is still running.
func TestLoginThrottleCountsParallelAttemptsAtAdmission(t *testing.T) {
	f := newLinkFixture(t, "owner@example.com", "correct horse battery")
	gate := &gatedHasher{PasswordHasher: f.svc.passwordHasher, release: make(chan struct{})}
	f.svc.passwordHasher = gate
	throttle, _ := clockedThrottle()
	f.svc.loginThrottle = throttle

	const parallel = 40
	var refused atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.Login("owner@example.com", "wrong guess", strangerIP)
			if errors.As(err, new(*app_errors.LoginThrottledError)) {
				refused.Add(1)
			}
		}()
	}
	deadline := time.Now().Add(10 * time.Second)
	for refused.Load()+gate.compared.Load() < parallel {
		if time.Now().After(deadline) {
			t.Fatalf("attempts did not settle: %d refused, %d comparing", refused.Load(), gate.compared.Load())
		}
		time.Sleep(time.Millisecond)
	}
	close(gate.release)
	wg.Wait()
	if got, want := int(gate.compared.Load()), loginThrottleFreeFailures+1; got != want {
		t.Fatalf("%d parallel attempts reached a password comparison, want %d", got, want)
	}
}

// The brake is keyed by the address as typed, so an address nobody holds is
// braked exactly like a registered one — it cannot reveal which exist.
func TestLoginThrottleTreatsUnknownAddressesAlike(t *testing.T) {
	f := newLoginTimingFixture(t, "owner@example.com", "correct horse battery")
	f.users.byEmail = nil
	throttle, _ := clockedThrottle()
	f.svc.loginThrottle = throttle

	for i := 0; i <= loginThrottleFreeFailures; i++ {
		_, _ = f.svc.Login("nobody@example.com", "guess", strangerIP)
	}
	_, err := f.svc.Login("nobody@example.com", "guess", strangerIP)
	if !errors.As(err, new(*app_errors.LoginThrottledError)) {
		t.Fatalf("an unknown address must be braked too, got %v", err)
	}
}

// The delay doubles and stops at the cap.
func TestLoginThrottleDelayDoublesUpToTheCap(t *testing.T) {
	throttle, now := clockedThrottle()
	const email = "owner@example.com"
	for i := 0; i < loginThrottleFreeFailures; i++ {
		if w := throttle.admit(email, strangerIP); w != 0 {
			t.Fatalf("free attempt %d refused for %v", i+1, w)
		}
	}
	var last time.Duration
	for i := 0; i < 12; i++ {
		if w := throttle.admit(email, strangerIP); w != 0 {
			t.Fatalf("attempt after the delay refused for %v", w)
		}
		w := throttle.admit(email, strangerIP)
		if w > loginThrottleMaxDelay {
			t.Fatalf("delay %v exceeds the cap %v", w, loginThrottleMaxDelay)
		}
		if w < last && last < loginThrottleMaxDelay {
			t.Fatalf("delay shrank from %v to %v before reaching the cap", last, w)
		}
		last = w
		*now = now.Add(w)
	}
	if last != loginThrottleMaxDelay {
		t.Fatalf("sustained failures settle at %v, want the cap %v", last, loginThrottleMaxDelay)
	}
	// Quiet for the reset window: forgotten.
	*now = now.Add(loginThrottleReset + time.Second)
	throttle.admit(email, strangerIP)
	if w := throttle.admit(email, strangerIP); w != 0 {
		t.Fatalf("after the reset window two attempts closed the address for %v", w)
	}
}

func TestLoginThrottleBoundsTrackedAddresses(t *testing.T) {
	throttle, _ := clockedThrottle()
	for i := 0; i < loginThrottleKeys+50; i++ {
		throttle.admit(fmt.Sprintf("a%d@example.com", i), strangerIP)
	}
	if n := len(throttle.pairs); n > loginThrottleKeys {
		t.Fatalf("tracking %d address and source pairs, cap is %d", n, loginThrottleKeys)
	}
	if n := len(throttle.addresses); n > loginThrottleKeys {
		t.Fatalf("tracking %d addresses, cap is %d", n, loginThrottleKeys)
	}
}

// A full map must not switch the brake off for an address it has not seen:
// a flood of single failures on junk addresses is evicted before a braked
// target, and a new target is still tracked.
func TestLoginThrottleKeepsBrakingWhenTheMapIsFull(t *testing.T) {
	throttle, now := clockedThrottle()
	for i := 0; i < loginThrottleKeys; i++ {
		throttle.admit(fmt.Sprintf("junk%d@example.com", i), strangerIP)
	}
	*now = now.Add(time.Second)
	const target = "target@example.com"
	for i := 0; i <= loginThrottleFreeFailures; i++ {
		throttle.admit(target, strangerIP)
	}
	if w := throttle.admit(target, strangerIP); w <= 0 {
		t.Fatal("a new address was not braked once the map was full")
	}
	// More junk does not evict the braked target.
	for i := 0; i < 100; i++ {
		throttle.admit(fmt.Sprintf("more%d@example.com", i), strangerIP)
	}
	if w := throttle.admit(target, strangerIP); w <= 0 {
		t.Fatal("the braked target was evicted by a flood of single failures")
	}
}
