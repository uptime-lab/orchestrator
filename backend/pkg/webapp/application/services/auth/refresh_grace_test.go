package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
)

var (
	thisBrowser  = refresh_token.NewDevice("Mozilla/5.0 (tab)", "198.51.100.10")
	otherBrowser = refresh_token.NewDevice("curl/8", "203.0.113.99")
)

func familyRevoked(f *switchFixture) bool {
	fam := f.originalRecord().FamilyID
	for _, tok := range f.store.byHash {
		if tok.FamilyID == fam && tok.RevokedAt != nil {
			return true
		}
	}
	return false
}

// Two tabs of one browser refreshing with the same token used to revoke the
// whole session, the winner's new tokens included. A second presentation by
// the same client right after the rotation now receives the successor the
// first one minted, and the session lives on.
func TestASecondRefreshFromTheSameClientGetsTheSameSuccessor(t *testing.T) {
	f := newSwitchFixture(t)

	first, err := f.svc.RefreshTokenFromDevice(f.original.RefreshToken, thisBrowser)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	second, err := f.svc.RefreshTokenFromDevice(f.original.RefreshToken, thisBrowser)
	if err != nil {
		t.Fatalf("the same client's second presentation inside the grace: %v", err)
	}
	if second.RefreshToken != first.RefreshToken || second.AccessToken != first.AccessToken {
		t.Fatal("the second presentation was not answered with the first one's successor")
	}
	if familyRevoked(f) {
		t.Fatal("the session family was revoked by a same-client race")
	}
	// The shared successor rotates normally afterwards.
	if _, err := f.svc.RefreshTokenFromDevice(first.RefreshToken, thisBrowser); err != nil {
		t.Fatalf("the successor no longer refreshes: %v", err)
	}
}

// Reuse detection is unchanged for anyone else: the same spent token from
// another client is a replay, and the family dies.
func TestASecondRefreshFromAnotherClientStillRevokesTheFamily(t *testing.T) {
	f := newSwitchFixture(t)

	if _, err := f.svc.RefreshTokenFromDevice(f.original.RefreshToken, thisBrowser); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	_, err := f.svc.RefreshTokenFromDevice(f.original.RefreshToken, otherBrowser)
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a replay from another client = %v, want ErrTokenInvalid", err)
	}
	if !familyRevoked(f) {
		t.Fatal("a replay from another client did not revoke the family")
	}
}

// ...and for the same client once the window has passed.
func TestAReplayAfterTheGraceStillRevokesTheFamily(t *testing.T) {
	f := newSwitchFixture(t)
	now := time.Now()
	f.svc.refreshGrace.now = func() time.Time { return now }

	if _, err := f.svc.RefreshTokenFromDevice(f.original.RefreshToken, thisBrowser); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	now = now.Add(refreshReplayGrace + time.Second)
	if _, err := f.svc.RefreshTokenFromDevice(f.original.RefreshToken, thisBrowser); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a replay after the grace = %v, want ErrTokenInvalid", err)
	}
	if !familyRevoked(f) {
		t.Fatal("a replay after the grace did not revoke the family")
	}
}

// A logout inside the window ends the grace with the session: the successor
// is not handed out again.
func TestTheGraceDoesNotOutliveALogout(t *testing.T) {
	f := newSwitchFixture(t)

	first, err := f.svc.RefreshTokenFromDevice(f.original.RefreshToken, thisBrowser)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := f.svc.RevokeSession(context.Background(), first.RefreshToken); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := f.svc.RefreshTokenFromDevice(f.original.RefreshToken, thisBrowser); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a presentation after logout = %v, want ErrTokenInvalid", err)
	}
}

// blockingRotate holds the first rotation open until released, so a second
// presentation arrives while the first is still in flight.
type blockingRotate struct {
	*fakeRefreshStore
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *blockingRotate) Rotate(ctx context.Context, hash string, issuedAt time.Time, gen int64, successor *refresh_token.RefreshToken) (*refresh_token.RefreshToken, error) {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	return b.fakeRefreshStore.Rotate(ctx, hash, issuedAt, gen, successor)
}

// The true concurrent case: the second presentation lands while the first
// is still rotating. It waits for the first and shares its successor.
func TestConcurrentRefreshesFromOneClientShareTheSuccessor(t *testing.T) {
	f := newSwitchFixture(t)
	blocking := &blockingRotate{fakeRefreshStore: f.store, entered: make(chan struct{}), release: make(chan struct{})}
	f.svc.refreshTokens = blocking

	var first *services.TokenPair
	var firstErr error
	done := make(chan struct{})
	go func() {
		first, firstErr = f.svc.RefreshTokenFromDevice(f.original.RefreshToken, thisBrowser)
		close(done)
	}()
	<-blocking.entered

	secondDone := make(chan struct{})
	var second *services.TokenPair
	var secondErr error
	go func() {
		second, secondErr = f.svc.RefreshTokenFromDevice(f.original.RefreshToken, thisBrowser)
		close(secondDone)
	}()
	time.Sleep(50 * time.Millisecond)
	close(blocking.release)
	<-done
	<-secondDone

	if firstErr != nil || secondErr != nil {
		t.Fatalf("concurrent refreshes: %v / %v", firstErr, secondErr)
	}
	if first.RefreshToken != second.RefreshToken {
		t.Fatal("the two concurrent presentations got different successors")
	}
	if familyRevoked(f) {
		t.Fatal("the family was revoked by two tabs refreshing together")
	}
}
