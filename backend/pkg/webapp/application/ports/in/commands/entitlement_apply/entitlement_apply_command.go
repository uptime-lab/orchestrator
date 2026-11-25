package entitlement_apply

import (
	"errors"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
)

// EntitlementApplyCommand applies a signed entitlement file to the install.
// The document is the paste form exactly as issued; everything else about
// the entitlement is a signed field inside it.
type EntitlementApplyCommand struct {
	Document    string               `json:"document"`
	AuthContext commands.AuthContext `json:"-"`
}

var (
	ErrDocumentMissing = errors.New("document is required")
	ErrDocumentTooLong = errors.New("document is too long to be an entitlement file")
)

// Validate checks the shape a handler can check before the verifier runs.
func (c EntitlementApplyCommand) Validate() error {
	if strings.TrimSpace(c.Document) == "" {
		return ErrDocumentMissing
	}
	if len(c.Document) > entitlement.MaxFormLength {
		return ErrDocumentTooLong
	}
	return nil
}
