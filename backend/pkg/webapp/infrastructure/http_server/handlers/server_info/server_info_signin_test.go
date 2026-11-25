package server_info

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

type fakeRegistration struct {
	state services.RegistrationState
	err   error
	asked int
}

func (f *fakeRegistration) RegistrationState(context.Context) (services.RegistrationState, error) {
	f.asked++
	return f.state, f.err
}

type fakeInstallKind bool

func (k fakeInstallKind) IsHostedInstall(context.Context) bool { return bool(k) }

// getJSON serves one request through the handler method and decodes the
// response's data field.
func getJSON(t *testing.T, serve func(echo.Context) error, path string) map[string]any {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	if err := serve(e.NewContext(req, rec)); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d", path, rec.Code)
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return body.Data
}

func mailConfig(configured bool) *config.Config {
	cfg := &config.Config{}
	if configured {
		cfg.Email = config.EmailConfig{Enabled: true, Host: "smtp.example.com", FromAddress: "noreply@example.com"}
	}
	return cfg
}

// The sign-in page shows "forgot password" only when a reset mail can be
// sent; an enabled mailer that is missing its host is NOT mail, the same
// rule the mailer itself applies.
func TestSignInOptionsReportsMailDelivery(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{"configured", mailConfig(true), true},
		{"not configured", mailConfig(false), false},
		{"enabled without a host", &config.Config{Email: config.EmailConfig{Enabled: true, FromAddress: "noreply@example.com"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(tc.cfg, false, slog.Default())
			data := getJSON(t, h.GetSignInOptions, "/api/v1/server/sign-in-options")
			if got, _ := data["email_delivery"].(bool); got != tc.want {
				t.Fatalf("email_delivery = %v, want %v", data["email_delivery"], tc.want)
			}
		})
	}
}

func TestSignInOptionsReportsTheRegistrationState(t *testing.T) {
	reg := &fakeRegistration{state: services.RegistrationFirstOwner}
	h := NewHandler(mailConfig(false), false, slog.Default())
	h.SetRegistration(reg)
	data := getJSON(t, h.GetSignInOptions, "/api/v1/server/sign-in-options")
	if data["registration"] != "first_owner" {
		t.Fatalf("registration = %v, want first_owner", data["registration"])
	}
}

// With the operator's open registration the page is told so, and it then
// shows the form without an invite code field.
func TestSignInOptionsReportsOpenRegistration(t *testing.T) {
	reg := &fakeRegistration{state: services.RegistrationOpen}
	h := NewHandler(mailConfig(false), false, slog.Default())
	h.SetRegistration(reg)
	data := getJSON(t, h.GetSignInOptions, "/api/v1/server/sign-in-options")
	if data["registration"] != "open" {
		t.Fatalf("registration = %v, want open", data["registration"])
	}
}

// Open registration on an install that cannot mail the confirmation is
// closed, and the page is told why; nowhere else is a reason given.
func TestSignInOptionsSaysWhyOpenRegistrationIsClosed(t *testing.T) {
	cfg := mailConfig(false)
	cfg.Server.OpenRegistration = true
	h := NewHandler(cfg, false, slog.Default())
	h.SetRegistration(&fakeRegistration{state: services.RegistrationClosed})
	data := getJSON(t, h.GetSignInOptions, "/api/v1/server/sign-in-options")
	if data["registration"] != "closed" || data["registration_reason"] != "email_unavailable" {
		t.Fatalf("open without mail: %v", data)
	}

	cfg.Server.OpenRegistration = false
	h = NewHandler(cfg, false, slog.Default())
	h.SetRegistration(&fakeRegistration{state: services.RegistrationClosed})
	if data := getJSON(t, h.GetSignInOptions, "/api/v1/server/sign-in-options"); data["registration_reason"] != nil {
		t.Fatalf("an ordinary closed install was given a reason: %v", data)
	}
}

// The page is tokenless, so the count behind it is cached: a burst of
// anonymous loads is one query, and the answer moves after the TTL.
func TestSignInOptionsCachesTheRegistrationRead(t *testing.T) {
	reg := &fakeRegistration{state: services.RegistrationInviteCode}
	h := NewHandler(mailConfig(false), false, slog.Default())
	h.SetRegistration(reg)
	clock := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return clock }

	for i := 0; i < 5; i++ {
		getJSON(t, h.GetSignInOptions, "/api/v1/server/sign-in-options")
	}
	if reg.asked != 1 {
		t.Fatalf("five loads inside the TTL read the count %d times, want 1", reg.asked)
	}

	reg.state = services.RegistrationClosed
	clock = clock.Add(registrationCacheTTL)
	data := getJSON(t, h.GetSignInOptions, "/api/v1/server/sign-in-options")
	if data["registration"] != "closed" || reg.asked != 2 {
		t.Fatalf("after the TTL: registration = %v, reads = %d; want closed, 2", data["registration"], reg.asked)
	}
}

// An unreadable count is "unknown", omitted, and not cached, so the page
// falls back to its ordinary form and the next load asks again.
func TestSignInOptionsOmitsAnUnreadableRegistrationState(t *testing.T) {
	reg := &fakeRegistration{err: errors.New("pool exhausted")}
	h := NewHandler(mailConfig(false), false, slog.Default())
	h.SetRegistration(reg)
	data := getJSON(t, h.GetSignInOptions, "/api/v1/server/sign-in-options")
	if _, present := data["registration"]; present {
		t.Fatalf("registration present on a failed read: %v", data["registration"])
	}
	getJSON(t, h.GetSignInOptions, "/api/v1/server/sign-in-options")
	if reg.asked != 2 {
		t.Fatalf("a failed read was cached: %d reads, want 2", reg.asked)
	}
}

// Nothing in the tokenless answer may name the install: no host, sender
// address or count travels with it. The fields are booleans and a state
// word, each something a visitor learns by trying.
func TestSignInOptionsCarriesNoInstallDetail(t *testing.T) {
	h := NewHandler(mailConfig(true), false, slog.Default())
	h.SetRegistration(&fakeRegistration{state: services.RegistrationInviteCode})
	data := getJSON(t, h.GetSignInOptions, "/api/v1/server/sign-in-options")
	for key := range data {
		if key != "email_delivery" && key != "registration" && key != "single_sign_on" {
			t.Fatalf("unexpected field %q in the tokenless sign-in options", key)
		}
	}
}

// account_erasure follows the install kind: only the hosted install has an
// operator console that performs the erasure a deletion request asks for.
func TestCapabilitiesReportAccountErasureFromTheInstallKind(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  InstallKindReporter
		wired bool
		want  bool
	}{
		{"hosted", fakeInstallKind(true), true, true},
		{"self-hosted", fakeInstallKind(false), true, false},
		{"not wired", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(&config.Config{}, false, slog.Default())
			if tc.wired {
				h.SetInstallKind(tc.kind)
			}
			data := getJSON(t, h.GetCapabilities, "/api/v1/server/capabilities")
			if got, _ := data["account_erasure"].(bool); got != tc.want {
				t.Fatalf("account_erasure = %v, want %v", data["account_erasure"], tc.want)
			}
		})
	}
}

// "first_owner" stops being true when the first account is created, possibly
// by another process; answering it from the cache refused every sign-up until
// the TTL ran out. It is read fresh on every load until an account exists.
func TestSignInOptionsNeverServesAStaleFirstOwnerAnswer(t *testing.T) {
	reg := &fakeRegistration{state: services.RegistrationFirstOwner}
	h := NewHandler(mailConfig(false), false, slog.Default())
	h.SetRegistration(reg)
	clock := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return clock }

	if data := getJSON(t, h.GetSignInOptions, "/api/v1/server/sign-in-options"); data["registration"] != "first_owner" {
		t.Fatalf("registration = %v, want first_owner", data["registration"])
	}
	// The owner is created; no time passes.
	reg.state = services.RegistrationInviteCode
	if data := getJSON(t, h.GetSignInOptions, "/api/v1/server/sign-in-options"); data["registration"] != "invite_code" {
		t.Fatalf("right after the first account: registration = %v, want invite_code", data["registration"])
	}
}

// The SPA asks GET /auth/oidc/config only where single sign-on is served;
// sign-in-options says which, and says false explicitly rather than omitting
// it, because an absent field is how an older backend answers.
func TestSignInOptionsReportsSingleSignOn(t *testing.T) {
	for _, on := range []bool{false, true} {
		h := NewHandler(mailConfig(false), false, slog.Default())
		h.SetSingleSignOn(on)
		data := getJSON(t, h.GetSignInOptions, "/api/v1/server/sign-in-options")
		got, present := data["single_sign_on"].(bool)
		if !present || got != on {
			t.Fatalf("single_sign_on = %v (present %v), want %v", data["single_sign_on"], present, on)
		}
	}
}
