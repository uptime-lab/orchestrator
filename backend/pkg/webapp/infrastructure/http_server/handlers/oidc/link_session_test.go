package oidc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

// Why these tests exist. The link ticket that carries "link my
// Google account" from the authenticated, step-up-guarded start route to the
// public IdP callback held only the user id. The callback then attached the
// identity without asking whether the session that started the link still
// existed — so a link begun by a session that was signed out, revoked
// everywhere or outlived by a password reset during the round trip still
// persisted a sign-in method that survives all three. The step-up flow
// carried its session proof through the same callback; the link flow did not.

// linkAuthStub answers the two calls the link arm makes and records them. The
// interface is embedded so anything else panics — the arm must not grow a
// third dependency unnoticed.
type linkAuthStub struct {
	services.AuthService
	liveErr error
	linkErr error
	asked   []services.SessionProof
	linked  []uuid.UUID
}

func (s *linkAuthStub) RequireLiveSession(_ context.Context, _ uuid.UUID, session services.SessionProof) error {
	s.asked = append(s.asked, session)
	return s.liveErr
}

func (s *linkAuthStub) LinkOIDC(_ context.Context, userID uuid.UUID, _ services.OIDCIdentity) error {
	s.linked = append(s.linked, userID)
	return s.linkErr
}

// LinkOIDCFromSession is what the callback calls now: the live-session test
// and the insert are one repository transition behind it, so the stub answers
// the session question first and links only when it passes.
func (s *linkAuthStub) LinkOIDCFromSession(ctx context.Context, userID uuid.UUID, identity services.OIDCIdentity, session services.SessionProof) error {
	if err := s.RequireLiveSession(ctx, userID, session); err != nil {
		return err
	}
	return s.LinkOIDC(ctx, userID, identity)
}

func driveLink(t *testing.T, stub *linkAuthStub, session services.SessionProof) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(nil, stub, config.OIDCConfig{}, "", false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?code=x&state=y", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	identity := services.OIDCIdentity{Provider: "google", Subject: "sub-1", Email: "u@example.com"}
	if err := h.completeLink(c, uuid.New(), session, identity); err != nil {
		t.Fatalf("completeLink returned %v", err)
	}
	return rec
}

func liveProof() services.SessionProof {
	return services.SessionProof{FamilyID: uuid.New(), IssuedAt: time.Now()}
}

func TestLinkCompletesForTheLiveSessionThatStartedIt(t *testing.T) {
	stub := &linkAuthStub{}
	proof := liveProof()
	rec := driveLink(t, stub, proof)
	if len(stub.linked) != 1 {
		t.Fatalf("LinkOIDC called %d times, want 1", len(stub.linked))
	}
	if len(stub.asked) != 1 || stub.asked[0].FamilyID != proof.FamilyID {
		t.Fatalf("the session proof handed to RequireLiveSession was %+v, want family %s", stub.asked, proof.FamilyID)
	}
	if loc := rec.Header().Get("Location"); !strings.HasSuffix(loc, "?oidc_link=ok") {
		t.Fatalf("redirect = %q, want …?oidc_link=ok", loc)
	}
}

func TestLinkIsRefusedWhenTheStartingSessionIsNoLongerLive(t *testing.T) {
	// Sign-out, "sign out everywhere", a family logout and a password reset all
	// surface here as ErrTokenInvalid from the auth service.
	stub := &linkAuthStub{liveErr: app_errors.ErrTokenInvalid}
	rec := driveLink(t, stub, liveProof())
	if len(stub.linked) != 0 {
		t.Fatal("an identity was attached for a session that had been revoked")
	}
	if loc := rec.Header().Get("Location"); !strings.HasSuffix(loc, "?oidc_link=error") {
		t.Fatalf("redirect = %q, want …?oidc_link=error", loc)
	}
}

func TestLinkFailsClosedWhenTheSessionCannotBeRead(t *testing.T) {
	stub := &linkAuthStub{liveErr: errors.New("pool timeout")}
	driveLink(t, stub, liveProof())
	if len(stub.linked) != 0 {
		t.Fatal("an identity was attached while the session state was unreadable")
	}
}

func TestLinkTicketCarriesTheSessionToTheCallback(t *testing.T) {
	// The proof is captured on the authenticated start route and consumed on
	// the public callback — it has to survive the round trip inside the ticket.
	h := NewHandler(nil, &linkAuthStub{}, config.OIDCConfig{}, "", false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	userID, proof := uuid.New(), liveProof()
	h.storeLinkTicket("code-1", userID, proof)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback", nil)
	req.AddCookie(&http.Cookie{Name: linkCookie, Value: "code-1"})
	c := e.NewContext(req, httptest.NewRecorder())
	gotUser, gotProof, ok := h.consumeLinkTicket(c)
	if !ok || gotUser != userID || gotProof.FamilyID != proof.FamilyID || !gotProof.IssuedAt.Equal(proof.IssuedAt) {
		t.Fatalf("consumed ticket = (%s, %+v, %v), want (%s, %+v, true)", gotUser, gotProof, ok, userID, proof)
	}
	if _, _, again := h.consumeLinkTicket(c); again {
		t.Fatal("a link ticket was consumable twice")
	}
}
