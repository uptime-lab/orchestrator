package results

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// AuthResult represents an authentication response
type AuthResult struct {
	User         *UserResult `json:"user"`
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	ExpiresIn    int64       `json:"expires_in"`
	TokenType    string      `json:"token_type"`
}

// NewAuthResult creates a new AuthResult from auth service response
func NewAuthResult(tokens *services.TokenPair, user *user.User) *AuthResult {
	return &AuthResult{
		User:         NewUserResult(user),
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
		TokenType:    tokens.TokenType,
	}
}

// AccountOptionResult is one account a user can select or switch into.
type AccountOptionResult struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	RequireTwoFactor bool   `json:"require_two_factor"`
}

// LoginChallengeResult is the login response of a multi-account user: no
// tokens yet, a short-lived pre-auth token plus the accounts to choose from.
// The client exchanges it via POST /auth/select-account.
type LoginChallengeResult struct {
	PreToken string                `json:"pre_token"`
	Accounts []AccountOptionResult `json:"accounts"`
}

// NewAccountOptionResults maps account options into their wire shape.
func NewAccountOptionResults(options []services.AccountOption) []AccountOptionResult {
	out := make([]AccountOptionResult, 0, len(options))
	for _, o := range options {
		out = append(out, AccountOptionResult{
			ID: o.ID.String(), Name: o.Name, RequireTwoFactor: o.RequireTwoFactor,
		})
	}
	return out
}

// TwoFactorChallengeResult is the response to a correct password when a second
// factor is still owed. It deliberately carries nothing but the ticket: no
// account list, no user profile — a stolen password must not even reveal which
// accounts the victim belongs to.
//
// TwoFactorMethods says how the ticket can be completed — "totp" via
// POST /auth/2fa, "passkey" via the ordinary passkey login — so the client can
// show the right step. It names enrolled factor KINDS, never a credential.
type TwoFactorChallengeResult struct {
	TwoFactorToken   string   `json:"two_factor_token"`
	TwoFactorMethods []string `json:"two_factor_methods"`
}

// NewTwoFactorChallengeResult builds the second-factor challenge response.
func NewTwoFactorChallengeResult(result *services.LoginResult) *TwoFactorChallengeResult {
	methods := result.TwoFactorMethods
	if methods == nil {
		methods = []string{}
	}
	return &TwoFactorChallengeResult{TwoFactorToken: result.TwoFactorToken, TwoFactorMethods: methods}
}

// NewLoginChallengeResult creates the account-selection login response.
func NewLoginChallengeResult(result *services.LoginResult) *LoginChallengeResult {
	return &LoginChallengeResult{
		PreToken: result.PreToken,
		Accounts: NewAccountOptionResults(result.Accounts),
	}
}

// TokenRefreshResult represents a token refresh response
type TokenRefreshResult struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// NewTokenRefreshResult creates a new TokenRefreshResult from token pair
func NewTokenRefreshResult(tokens *services.TokenPair) *TokenRefreshResult {
	return &TokenRefreshResult{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
		TokenType:    tokens.TokenType,
	}
}
