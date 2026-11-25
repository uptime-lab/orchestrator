package commands

import "github.com/google/uuid"

// AuthContext represents the authentication context for commands and queries
type AuthContext struct {
	AccountID uuid.UUID `json:"-"`
	UserID    uuid.UUID `json:"-"`
}

// NewAuthContext creates a new AuthContext with the provided IDs
func NewAuthContext(accountID, userID uuid.UUID) AuthContext {
	return AuthContext{
		AccountID: accountID,
		UserID:    userID,
	}
}

// GetAccountID returns the account ID from the auth context
func (a AuthContext) GetAccountID() uuid.UUID {
	return a.AccountID
}

// GetUserID returns the user ID from the auth context
func (a AuthContext) GetUserID() uuid.UUID {
	return a.UserID
}

// IsValid checks if the AuthContext has valid UUIDs
func (a *AuthContext) IsValid() bool {
	return a.AccountID != uuid.Nil && a.UserID != uuid.Nil
}
