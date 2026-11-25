package node

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// ProvisioningIncompleteError refuses a provision_connection whose node
// resolves no account to create: no username, no way to log into it, or a
// stored password that no configured key decrypts. The command build refuses
// the same configurations; refusing at dispatch is what keeps the request from
// answering "dispatched" for a command that can only fail.
type ProvisioningIncompleteError struct {
	NodeID  uuid.UUID
	Missing string
}

func (e *ProvisioningIncompleteError) Error() string {
	return fmt.Sprintf("provision_connection cannot run for node %s: %s", e.NodeID, e.Missing)
}

// The request is well-formed; the node's provisioning policy refuses it.
func (e *ProvisioningIncompleteError) HTTPStatus() int   { return http.StatusConflict }
func (e *ProvisioningIncompleteError) ErrorCode() string { return "provisioning_incomplete" }

var _ results.OperatorFacingError = (*ProvisioningIncompleteError)(nil)

// checkProvisionConnection resolves the node's effective provisioning the way
// the command build does and refuses, before a command row exists, what the
// build would refuse. A resolver that is not wired or fails defers to the
// build, which stays the authority: a database blip must not block a
// correctly configured node.
func (s *Service) checkProvisionConnection(ctx context.Context, accountID uuid.UUID, target *node.Node) error {
	if s.accessGeneration == nil || s.accessGeneration.ou == nil {
		return nil
	}
	eff, err := s.accessGeneration.ou.ResolveEffectiveProvisioning(ctx, accountID, target.ID)
	if err != nil {
		s.logger.WarnContext(ctx, "could not resolve the node's provisioning policy for provision_connection; deferring the check to dispatch",
			"node_id", target.ID, "error", err)
		return nil
	}
	switch {
	case eff.Username == "":
		return &ProvisioningIncompleteError{NodeID: target.ID, Missing: "no OS username is set on the node, " +
			"its organizational unit or the account. Set a username and a password or an SSH key, then run it again"}
	case eff.PasswordUnreadable:
		return &ProvisioningIncompleteError{NodeID: target.ID, Missing: "the stored provisioning password cannot be " +
			"decrypted with the configured key. Set it again (or restore the key), then run it again"}
	case eff.Password == "" && eff.SSHKey == "":
		return &ProvisioningIncompleteError{NodeID: target.ID, Missing: fmt.Sprintf("the user %q has no password "+
			"and no SSH key, so it would be created with no way to log in. Set a password or an SSH key, then run it again",
			eff.Username)}
	}
	return nil
}
