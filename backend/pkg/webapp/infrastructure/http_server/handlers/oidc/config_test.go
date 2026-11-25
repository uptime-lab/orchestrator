package oidc

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

func serveConfig(t *testing.T, cfg config.OIDCConfig, inviteCode string) map[string]any {
	t.Helper()
	return serveConfigWith(t, cfg, inviteCode, false)
}

func serveConfigWith(t *testing.T, cfg config.OIDCConfig, inviteCode string, openRegistration bool) map[string]any {
	t.Helper()
	h := NewHandler(nil, nil, cfg, inviteCode, openRegistration, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e := echo.New()
	rec := httptest.NewRecorder()
	if err := h.Config(e.NewContext(httptest.NewRequest(http.MethodGet, "/auth/oidc/config", nil), rec)); err != nil {
		t.Fatalf("Config: %v", err)
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Data
}

// The SPA names the provider from this answer, and shows the invite field
// only where the callback would ask for the code. Both must follow the same
// rules the backend applies, or the page offers a sign-up that is refused.
func TestOIDCConfigNamesTheProviderAndTheSignUpRule(t *testing.T) {
	google := config.OIDCConfig{IssuerURL: "https://accounts.google.com"}
	other := config.OIDCConfig{IssuerURL: "https://id.example.com", ProviderName: "Keycloak"}

	data := serveConfig(t, google, "the-code")
	if data["provider"] != "Google" || data["issuer"] != "https://accounts.google.com" || data["sign_up"] != "invite_code" {
		t.Fatalf("google with a code: %v", data)
	}

	data = serveConfig(t, other, "")
	if data["provider"] != "Keycloak" || data["sign_up"] != "closed" {
		t.Fatalf("named provider, no code, no auto-provision: %v", data)
	}

	other.AutoProvision = true
	data = serveConfig(t, other, "")
	if data["sign_up"] != "open" {
		t.Fatalf("auto-provision without a code: sign_up = %v, want open", data["sign_up"])
	}

	// A configured code gates provisioning even with auto-provision on,
	// exactly as Callback decides.
	data = serveConfig(t, other, "the-code")
	if data["sign_up"] != "invite_code" {
		t.Fatalf("auto-provision with a code: sign_up = %v, want invite_code", data["sign_up"])
	}
}

// Open registration is the instance's gate, so it decides the provider path
// the way the invite code does — ahead of OIDC_AUTO_PROVISION — and the
// config answer and the callback's decision agree on every combination.
func TestOpenRegistrationOpensTheProviderPath(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		name          string
		open          bool
		code          string
		autoProvision bool
		cookie        string
		wantMode      string
		wantAllow     bool
		wantGated     bool
	}{
		{"open, nothing else", true, "", false, "", "open", true, false},
		{"open wins over a code, no cookie", true, "the-code", false, "", "open", true, false},
		{"open wins over a code, wrong cookie", true, "the-code", false, "wrong", "open", true, false},
		{"open with auto-provision", true, "", true, "", "open", true, false},
		{"closed: code, no cookie", false, "the-code", true, "", "invite_code", false, true},
		{"code with the right cookie", false, "the-code", false, "the-code", "invite_code", true, true},
		{"auto-provision alone", false, "", true, "", "open", true, false},
		{"nothing configured", false, "", false, "", "closed", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.OIDCConfig{IssuerURL: "https://id.example.com", AutoProvision: tc.autoProvision}
			if got := serveConfigWith(t, cfg, tc.code, tc.open)["sign_up"]; got != tc.wantMode {
				t.Fatalf("sign_up = %v, want %s", got, tc.wantMode)
			}
			h := NewHandler(nil, nil, cfg, tc.code, tc.open, logger)
			req := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback", nil)
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: inviteCookie, Value: tc.cookie})
			}
			allow, gated := h.provisionDecision(echo.New().NewContext(req, httptest.NewRecorder()))
			if allow != tc.wantAllow || gated != tc.wantGated {
				t.Fatalf("provisionDecision = (allow %v, gated %v), want (%v, %v)", allow, gated, tc.wantAllow, tc.wantGated)
			}
		})
	}
}
