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
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// deviceRecordingAuth answers the one call a successful /complete makes.
type deviceRecordingAuth struct {
	services.AuthService
	linkCalls int
}

func (*deviceRecordingAuth) RecordSessionDevice(context.Context, string, refresh_token.Device) {}

func (a *deviceRecordingAuth) LinkOIDCWithPassword(context.Context, services.OIDCIdentity, string, string) (*services.LoginResult, error) {
	a.linkCalls++
	return &services.LoginResult{PreToken: "pre", Accounts: []services.AccountOption{}}, nil
}

func newBindingHandler(auth services.AuthService) *Handler {
	return &Handler{
		authService:  auth,
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		handoffs:     map[string]handoff{},
		pendingLinks: map[string]pendingLink{},
	}
}

func postJSON(t *testing.T, h echo.HandlerFunc, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	if err := h(echo.New().NewContext(req, rec)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	return rec
}

func loginResult() *services.LoginResult {
	return &services.LoginResult{
		Tokens: &services.TokenPair{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer"},
		User:   user.NewUser("someone@example.com", "Someone", ""),
	}
}

// A handoff code redeems only together with the binding cookie the callback
// set in the browser that finished the round-trip. A code planted in another
// browser (a link to /oidc-callback?code=...) must not sign that browser in,
// and the attempt spends the code.
func TestCompleteRequiresTheBrowserBinding(t *testing.T) {
	cases := []struct {
		name   string
		cookie *http.Cookie
		want   int
	}{
		{"no binding cookie", nil, http.StatusUnauthorized},
		{"another browser's binding", &http.Cookie{Name: bindingCookie, Value: "someone-else"}, http.StatusUnauthorized},
		{"the binding set with the code", &http.Cookie{Name: bindingCookie, Value: "this-browser"}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newBindingHandler(&deviceRecordingAuth{})
			h.storeHandoff("handoff-code", "this-browser", loginResult())

			rec := postJSON(t, h.Complete, "/auth/oidc/complete", `{"code":"handoff-code"}`, tc.cookie)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			// Single-use either way: a refused presentation does not leave the
			// code for a second try.
			if _, ok := h.handoffs["handoff-code"]; ok {
				t.Fatal("the handoff code survived its presentation")
			}
		})
	}
}

// A stored code with no binding never redeems, even against an empty cookie.
func TestBindingMatchesRefusesEmptyValues(t *testing.T) {
	if bindingMatches("", "") || bindingMatches("x", "") || bindingMatches("", "x") {
		t.Fatal("an empty binding must never match")
	}
	if !bindingMatches("x", "x") {
		t.Fatal("equal bindings must match")
	}
}

// The pending-link code is bound the same way: without the cookie the
// password is never even checked.
func TestLinkExistingRequiresTheBrowserBinding(t *testing.T) {
	auth := &deviceRecordingAuth{}
	h := newBindingHandler(auth)
	h.storePendingLink("link-code", "this-browser", services.OIDCIdentity{Provider: "p", Subject: "s", Email: "someone@example.com"})

	rec := postJSON(t, h.LinkExisting, "/auth/oidc/link-existing", `{"link_code":"link-code","password":"pw"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d without the binding, want 401", rec.Code)
	}
	if auth.linkCalls != 0 {
		t.Fatal("the password was checked for a link code presented from another browser")
	}

	h.storePendingLink("link-code-2", "this-browser", services.OIDCIdentity{Provider: "p", Subject: "s", Email: "someone@example.com"})
	rec = postJSON(t, h.LinkExisting, "/auth/oidc/link-existing", `{"link_code":"link-code-2","password":"pw"}`,
		&http.Cookie{Name: bindingCookie, Value: "this-browser"})
	if rec.Code != http.StatusAccepted || auth.linkCalls != 1 {
		t.Fatalf("with the binding: status %d, link calls %d; want 202 and 1", rec.Code, auth.linkCalls)
	}
}

// The callback's redirect carries the cookie: httpOnly, Lax, path /.
func TestIssueBrowserBindingSetsAnHTTPOnlyCookie(t *testing.T) {
	h := newBindingHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback", nil)
	rec := httptest.NewRecorder()
	value, err := h.issueBrowserBinding(echo.New().NewContext(req, rec))
	if err != nil || value == "" {
		t.Fatalf("issueBrowserBinding: %q, %v", value, err)
	}
	var found *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == bindingCookie {
			found = ck
		}
	}
	if found == nil || found.Value != value || !found.HttpOnly || found.SameSite != http.SameSiteLaxMode || found.Path != "/" {
		t.Fatalf("binding cookie = %+v, want httpOnly Lax at / carrying %q", found, value)
	}
}
