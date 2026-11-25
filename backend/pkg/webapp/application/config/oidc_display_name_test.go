package config

import "testing"

// The SPA words the sign-in button and the account page from DisplayName,
// so a self-hosted provider must never be called Google, and Google must
// stay Google without anyone configuring a name.
func TestOIDCDisplayName(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       OIDCConfig
		wantName  string
		wantLabel string
	}{
		{"google issuer", OIDCConfig{IssuerURL: "https://accounts.google.com"}, "Google", "Sign in with Google"},
		{"other issuer, no name", OIDCConfig{IssuerURL: "https://id.example.com/realms/lab"}, "", "Sign in with SSO"},
		{"other issuer, named", OIDCConfig{IssuerURL: "https://id.example.com/realms/lab", ProviderName: "Keycloak"}, "Keycloak", "Sign in with Keycloak"},
		{"google issuer, renamed", OIDCConfig{IssuerURL: "https://accounts.google.com", ProviderName: "Example Workspace"}, "Example Workspace", "Sign in with Example Workspace"},
		{"blank name falls back", OIDCConfig{IssuerURL: "https://accounts.google.com", ProviderName: "   "}, "Google", "Sign in with Google"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.DisplayName(); got != tc.wantName {
				t.Fatalf("DisplayName = %q, want %q", got, tc.wantName)
			}
			if got := tc.cfg.ButtonLabel(); got != tc.wantLabel {
				t.Fatalf("ButtonLabel = %q, want %q", got, tc.wantLabel)
			}
		})
	}
}

func TestOIDCProviderNameIsReadFromTheEnvironment(t *testing.T) {
	t.Setenv("OIDC_PROVIDER_NAME", "Keycloak")
	cfg, err := ApplyEnvironment(&Config{})
	if err != nil {
		t.Fatalf("ApplyEnvironment: %v", err)
	}
	if cfg.OIDC.ProviderName != "Keycloak" {
		t.Fatalf("ProviderName = %q, want Keycloak", cfg.OIDC.ProviderName)
	}
}
