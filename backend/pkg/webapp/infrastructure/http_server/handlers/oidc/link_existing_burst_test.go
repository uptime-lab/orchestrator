package oidc

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

// recordingAuthService answers LinkOIDCWithPassword with a fixed error and
// remembers the failures reported to the burst detector. The interface is
// embedded so every other method it carries panics rather than silently
// answering — this test must fail if the handler starts calling something else.
type recordingAuthService struct {
	services.AuthService
	linkErr  error
	notedIPs []string
}

func (s *recordingAuthService) LinkOIDCWithPassword(context.Context, services.OIDCIdentity, string, string) (*services.LoginResult, error) {
	return nil, s.linkErr
}

func (s *recordingAuthService) NoteFailedLogin(_ context.Context, sourceIP string) {
	s.notedIPs = append(s.notedIPs, sourceIP)
}

// POST /auth/oidc/link-existing verifies a password, so a rejection there has
// to reach the credential-stuffing detector like the login form, the passkey
// ceremony and the step-up confirmations do. It was the one password-verifying
// endpoint that reported nothing, which is exactly the door an attacker picks:
// the per-IP limiter slows the guessing down, and the operator sees nothing.
//
// The second case is the other half of the rule. Only a credential rejection is
// a guess; a conflict or a storage failure after the password was accepted must
// not raise an auth-burst anomaly, or ordinary breakage looks like an attack.
func TestLinkExistingReportsRejectedPasswordToBurstDetector(t *testing.T) {
	cases := []struct {
		name      string
		linkErr   error
		wantNoted bool
	}{
		{
			name:      "wrong password is a failed credential attempt",
			linkErr:   app_errors.ErrInvalidCredentials,
			wantNoted: true,
		},
		{
			name:      "a conflict past the password check is not a guess",
			linkErr:   app_errors.ErrExternalIdentityConflict,
			wantNoted: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := &recordingAuthService{linkErr: tc.linkErr}
			h := &Handler{
				authService:  auth,
				logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
				pendingLinks: map[string]pendingLink{},
			}
			const code = "link-code"
			h.storePendingLink(code, "browser-binding", services.OIDCIdentity{Provider: "google", Subject: "sub", Email: "victim@example.com"})

			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/auth/oidc/link-existing",
				strings.NewReader(`{"link_code":"`+code+`","password":"guess"}`))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			req.RemoteAddr = "203.0.113.7:51234"
			req.AddCookie(&http.Cookie{Name: bindingCookie, Value: "browser-binding"})
			rec := httptest.NewRecorder()

			if err := h.LinkExisting(e.NewContext(req, rec)); err != nil {
				t.Fatalf("handler returned an error: %v", err)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}

			switch {
			case tc.wantNoted && len(auth.notedIPs) == 0:
				t.Fatal("a rejected password on the OIDC link endpoint was not reported to the burst detector")
			case !tc.wantNoted && len(auth.notedIPs) != 0:
				t.Fatalf("a non-credential failure was counted as an authentication failure: %v", auth.notedIPs)
			}
			if tc.wantNoted && auth.notedIPs[0] != "203.0.113.7" {
				t.Fatalf("reported source %q, want the client address", auth.notedIPs[0])
			}
		})
	}
}
