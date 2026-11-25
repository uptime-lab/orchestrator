package auth

import (
	"context"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/google/uuid"
)

// login mints a fresh session (a new family) the way a real sign-in does.
func login(t *testing.T, svc *AuthService, usr *user.User) string {
	t.Helper()
	pair, err := svc.GenerateTokens(usr, uuid.New())
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	return pair.RefreshToken
}

func TestEachLoginIsItsOwnSessionAndRotationIsNot(t *testing.T) {
	store := newFakeRefreshStore()
	svc := newTestAuthServiceWithStore(store)
	usr := &user.User{ID: uuid.New(), Email: "a@b.c"}

	first := login(t, svc, usr)
	login(t, svc, usr)

	sessions, err := svc.ListSessions(context.Background(), usr.ID, uuid.Nil)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("two logins are two sessions, got %d", len(sessions))
	}

	// Rotating a token stays inside its family: still two sessions, not three.
	// (generateTokens directly, because the full RefreshToken path needs a user
	// repository this fixture deliberately does not have.)
	rotated := store.byHash[refresh_token.Hash(first)]
	if _, err := svc.generateTokens(usr, rotated.AccountID, rotated.FamilyID); err != nil {
		t.Fatalf("rotation failed: %v", err)
	}
	sessions, _ = svc.ListSessions(context.Background(), usr.ID, uuid.Nil)
	if len(sessions) != 2 {
		t.Fatalf("a rotation must not create a session, got %d", len(sessions))
	}
}

func TestCurrentSessionIsMarkedFromTheAccessToken(t *testing.T) {
	svc := newTestAuthServiceWithStore(newFakeRefreshStore())
	usr := &user.User{ID: uuid.New(), Email: "a@b.c"}

	pair, err := svc.GenerateTokens(usr, uuid.New())
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	login(t, svc, usr)

	// The ACCESS token has to carry the family, or a caller cannot tell which
	// of its own sessions it is talking to us with.
	claims, err := svc.ValidateAccessToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	if claims.FamilyID == uuid.Nil {
		t.Fatal("the access token must name its session")
	}

	sessions, _ := svc.ListSessions(context.Background(), usr.ID, claims.FamilyID)
	current := 0
	for _, s := range sessions {
		if s.Current {
			current++
			if s.FamilyID != claims.FamilyID {
				t.Error("the wrong session was marked current")
			}
		}
	}
	if current != 1 {
		t.Fatalf("exactly one session is current, got %d", current)
	}
}

// An access token minted before family ids existed names no session. It must
// match NOTHING — treating uuid.Nil as a match would mark every session as the
// current one and make "sign out everywhere else" a no-op.
func TestUnknownSessionMatchesNothing(t *testing.T) {
	svc := newTestAuthServiceWithStore(newFakeRefreshStore())
	usr := &user.User{ID: uuid.New(), Email: "a@b.c"}
	login(t, svc, usr)

	sessions, _ := svc.ListSessions(context.Background(), usr.ID, uuid.Nil)
	for _, s := range sessions {
		if s.Current {
			t.Fatal("a caller that cannot name its session must not match one")
		}
	}
}

func TestRevokeOtherSessionsKeepsTheCaller(t *testing.T) {
	store := newFakeRefreshStore()
	svc := newTestAuthServiceWithStore(store)
	usr := &user.User{ID: uuid.New(), Email: "a@b.c"}

	keep, err := svc.GenerateTokens(usr, uuid.New())
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	claims, _ := svc.ValidateAccessToken(keep.AccessToken)
	login(t, svc, usr)
	login(t, svc, usr)

	if err := svc.RevokeOtherSessions(context.Background(), usr.ID, claims.FamilyID); err != nil {
		t.Fatalf("revoke failed: %v", err)
	}
	sessions, _ := svc.ListSessions(context.Background(), usr.ID, claims.FamilyID)
	if len(sessions) != 1 || sessions[0].FamilyID != claims.FamilyID {
		t.Fatalf("only the caller's session survives, got %v", sessions)
	}
	// And it is still live: this is the "I lost my phone" button, not a logout.
	if store.byHash[refresh_token.Hash(keep.RefreshToken)].RevokedAt != nil {
		t.Error("the kept session's token must not have been revoked")
	}
}

func TestRevokeSessionRefusesSomebodyElses(t *testing.T) {
	store := newFakeRefreshStore()
	svc := newTestAuthServiceWithStore(store)
	mine := &user.User{ID: uuid.New(), Email: "a@b.c"}
	theirs := &user.User{ID: uuid.New(), Email: "d@e.f"}
	login(t, svc, mine)
	login(t, svc, theirs)

	victim, _ := svc.ListSessions(context.Background(), theirs.ID, uuid.Nil)
	if err := svc.RevokeSessionFamily(context.Background(), mine.ID, victim[0].FamilyID); err == nil {
		t.Fatal("a session id is handed to clients; revoking it must be scoped to its owner")
	}
	still, _ := svc.ListSessions(context.Background(), theirs.ID, uuid.Nil)
	if len(still) != 1 {
		t.Fatal("the other user's session must survive")
	}
}

// The cap evicts the least recently active session instead of refusing the
// login: blocking sign-in would turn a forgotten session into a lockout.
func TestSessionCapEvictsTheOldestRatherThanRefusingTheLogin(t *testing.T) {
	store := newFakeRefreshStore()
	svc := newTestAuthServiceWithStore(store)
	svc.SetSessionCap(2)
	usr := &user.User{ID: uuid.New(), Email: "a@b.c"}

	oldest := login(t, svc, usr)
	login(t, svc, usr)
	newest, err := svc.GenerateTokens(usr, uuid.New())
	if err != nil {
		t.Fatalf("the login past the cap must still succeed: %v", err)
	}

	sessions, _ := svc.ListSessions(context.Background(), usr.ID, uuid.Nil)
	if len(sessions) != 2 {
		t.Fatalf("the cap must hold, got %d sessions", len(sessions))
	}
	if store.byHash[refresh_token.Hash(oldest)].RevokedAt == nil {
		t.Error("the least recently active session must have been revoked")
	}
	if store.byHash[refresh_token.Hash(newest.RefreshToken)].RevokedAt != nil {
		t.Error("the session that just logged in must survive")
	}
}

func TestSessionCapZeroIsUnlimited(t *testing.T) {
	svc := newTestAuthServiceWithStore(newFakeRefreshStore())
	usr := &user.User{ID: uuid.New(), Email: "a@b.c"}
	for i := 0; i < 5; i++ {
		login(t, svc, usr)
	}
	sessions, _ := svc.ListSessions(context.Background(), usr.ID, uuid.Nil)
	if len(sessions) != 5 {
		t.Fatalf("an unset cap must not evict anything, got %d", len(sessions))
	}
}

func TestRecordSessionDeviceLabelsTheLogin(t *testing.T) {
	store := newFakeRefreshStore()
	svc := newTestAuthServiceWithStore(store)
	usr := &user.User{ID: uuid.New(), Email: "a@b.c"}
	token := login(t, svc, usr)

	svc.RecordSessionDevice(context.Background(), token,
		refresh_token.NewDevice("Mozilla/5.0 (Macintosh)", "203.0.113.7"))

	sessions, _ := svc.ListSessions(context.Background(), usr.ID, uuid.Nil)
	if sessions[0].UserAgent != "Mozilla/5.0 (Macintosh)" || sessions[0].IP != "203.0.113.7" {
		t.Fatalf("the session was not labelled: %+v", sessions[0])
	}
}

func TestDeviceStringsAreTruncated(t *testing.T) {
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	d := refresh_token.NewDevice(string(long), string(long))
	if len(d.UserAgent) != 255 || len(d.IP) != 64 {
		t.Fatalf("a User-Agent is attacker-controlled free text and must be bounded: %d/%d",
			len(d.UserAgent), len(d.IP))
	}
}
