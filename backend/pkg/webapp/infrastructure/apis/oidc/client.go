// Package oidc wraps go-oidc + oauth2 into a small provider client: build the
// authorize URL, exchange the code, and verify the returned id_token. It is
// provider-agnostic (Google is just the accounts.google.com issuer); an
// enterprise points the same config at their own IdP.
package oidc

import (
	"context"
	"fmt"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// VerifiedIdentity is the trustworthy subset of a verified id_token.
type VerifiedIdentity struct {
	Provider string // the issuer (stable per IdP)
	Subject  string // `sub` claim (stable per user per issuer)
	Email    string // verified email (lowercased by the caller)
	Name     string
}

// Client is a configured OIDC relying party.
type Client struct {
	provider     *oidc.Provider
	verifier     *oidc.IDTokenVerifier
	oauth2Config oauth2.Config
}

// NewClient discovers the issuer's metadata and builds the RP. Returns an error
// if discovery fails (bad issuer / no network at startup).
func NewClient(ctx context.Context, issuerURL, clientID, clientSecret, redirectURL string) (*Client, error) {
	provider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery failed for %s: %w", issuerURL, err)
	}
	return &Client{
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID}),
		oauth2Config: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		},
	}, nil
}

// AuthCodeURL builds the IdP authorization URL for the given CSRF state and
// nonce. The nonce is echoed in the id_token and checked at exchange time.
func (c *Client) AuthCodeURL(state, nonce string) string {
	return c.oauth2Config.AuthCodeURL(state, oidc.Nonce(nonce))
}

// AuthCodeURLForStepUp builds the same URL, but demands that the IdP
// re-authenticate the person NOW rather than answering from an existing session
// there.
//
// prompt=login and max_age=0 both say it; sending both is deliberate, because
// providers honour them unevenly. Without this a "step-up" through an IdP the
// browser is already signed into is a redirect that proves nothing — exactly
// the gap that lets a stolen credentialless session enrol its own authenticator.
func (c *Client) AuthCodeURLForStepUp(state, nonce string) string {
	return c.oauth2Config.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("prompt", "login"),
		oauth2.SetAuthURLParam("max_age", "0"),
	)
}

// Exchange trades the callback code for tokens, verifies the id_token
// (signature via JWKS, issuer, audience, expiry), checks the nonce and
// email_verified, and returns the trustworthy identity.
func (c *Client) Exchange(ctx context.Context, code, expectedNonce string) (*VerifiedIdentity, error) {
	token, err := c.oauth2Config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("%w: code exchange failed: %v", app_errors.ErrOIDCVerificationFailed, err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, fmt.Errorf("%w: no id_token in response", app_errors.ErrOIDCVerificationFailed)
	}
	idToken, err := c.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("%w: id_token verification failed: %v", app_errors.ErrOIDCVerificationFailed, err)
	}
	if idToken.Nonce != expectedNonce {
		return nil, fmt.Errorf("%w: nonce mismatch", app_errors.ErrOIDCVerificationFailed)
	}

	var claims struct {
		Subject       string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("%w: cannot parse id_token claims: %v", app_errors.ErrOIDCVerificationFailed, err)
	}
	if claims.Email == "" || !claims.EmailVerified {
		return nil, app_errors.ErrOIDCEmailNotVerified
	}

	return &VerifiedIdentity{
		Provider: idToken.Issuer,
		Subject:  claims.Subject,
		Email:    claims.Email,
		Name:     claims.Name,
	}, nil
}
