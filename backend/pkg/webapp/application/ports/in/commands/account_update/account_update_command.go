package account_update

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// Account name bounds, mirroring signup.
const (
	MinNameLength = 2
	MaxNameLength = 100
)

// AccountUpdateCommand represents a command to update an account
type AccountUpdateCommand struct {
	ID          uuid.UUID            `json:"id"`
	AuthContext commands.AuthContext `json:"-"`
	// Name is a POINTER (the NodeUpdateCommand doctrine): omitted means
	// "unchanged". It used to be required, which made every settings toggle
	// (2FA policy, identify duration) carry the name captured at page mount —
	// and silently revert a rename another operator saved in between.
	Name *string `json:"name,omitempty"`
	// RequireTwoFactor makes a second factor mandatory for every member who
	// signs in with a password; a member with no password (Google only) is
	// never challenged on that path and so is not asked to enrol.
	// A POINTER so an update that only renames the account does not silently
	// switch the policy off: absent means "leave as is".
	RequireTwoFactor *bool `json:"require_two_factor,omitempty"`
	// IdentifyDurationSec sets how long identify blinks run (10..3600 s).
	IdentifyDurationSec *int `json:"identify_duration_sec,omitempty"`
}

// Validate checks the command invariants. A present Name is trimmed in place
// so a whitespace-padded rename cannot slip past the length bounds; an absent
// one is "unchanged" and validates nothing.
func (c *AccountUpdateCommand) Validate() error {
	if c.ID == uuid.Nil {
		return fmt.Errorf("account ID is required")
	}
	if c.Name == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*c.Name)
	c.Name = &trimmed
	// Count runes, not bytes: a 100-character name in a non-Latin script is
	// still a valid name.
	length := utf8.RuneCountInString(trimmed)
	if length < MinNameLength {
		return fmt.Errorf("account name must be at least %d characters", MinNameLength)
	}
	if length > MaxNameLength {
		return fmt.Errorf("account name must be at most %d characters", MaxNameLength)
	}
	return nil
}
