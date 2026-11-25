// Package account_export assembles everything an account owns into one
// document, so a tenant can leave with their configuration instead of only
// their memory of it.
//
// It is a COMPOSITION, not a new read path: every source below is a method the
// API already exposes to the same caller. That is deliberate and is the whole
// tenancy argument — each source takes the account id and scopes its own query,
// so the export inherits the isolation those paths already have rather than
// introducing a second, parallel way to read a tenant's rows.
//
// What it never carries is credential material: no secret values, no SSH
// private keys, no access-entry passwords, no adoption tokens, no master
// tokens. The same rule the audit trail follows (AuditEvent.Detail), and for a
// sharper reason here — this document is written to be copied off the platform
// and mailed around, and a `SECRETS_ENCRYPTION_KEY` rotation cannot reach a file
// on someone's laptop. Secret NAMES are the identifiers playbooks reference, so
// they are the useful half and they are safe; the values have their own path
// (recreate them at the destination), exactly as the write-only secrets API
// already requires.
package account_export

import (
	"context"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/organizational_unit_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/secret_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/ssh_key_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/workflow_template_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// Each source is named and separate rather than one wide interface, so a caller
// that cannot supply one (a test, or a future install with a feature disabled)
// omits exactly that section instead of failing to construct the service.

// AccountSource reads the account record itself.
type AccountSource interface {
	GetAccount(ctx context.Context, id uuid.UUID) (*account.Account, error)
}

// MemberSource lists who has access to the account and with what bindings.
type MemberSource interface {
	List(ctx context.Context, auth commands.AuthContext) ([]member.Member, error)
}

// OrganizationalUnitSource lists the OU tree and the account-wide policy the
// tree falls back to.
type OrganizationalUnitSource interface {
	GetAllByAccountID(ctx context.Context, query organizational_unit_list.OrganizationalUnitListQuery) ([]organizational_unit.OrganizationalUnit, error)
	GetAccountPolicy(ctx context.Context, accountID uuid.UUID) (*organizational_unit.AccountPolicy, error)
}

// NodeSource lists the fleet.
type NodeSource interface {
	GetAllByAccountID(ctx context.Context, query node_list.NodeListQuery) ([]node.Node, error)
}

// AccessSource lists declarative shell access — users, CAs and their scopes.
type AccessSource interface {
	ListAllEntries(ctx context.Context, accountID uuid.UUID) ([]access_entry.AccessEntry, error)
}

// SSHKeySource lists the account's SSH keys. Only the PUBLIC half is exported;
// see the package comment.
type SSHKeySource interface {
	GetAllByAccountID(ctx context.Context, query ssh_key_list.SSHKeyListQuery) ([]ssh_key.SSHKey, error)
}

// SecretSource lists secrets and their attachments. Values are exported only
// for non-sensitive variables, which is the same split the API already applies.
type SecretSource interface {
	List(ctx context.Context, query secret_list.SecretListQuery) ([]secret.Secret, int64, error)
	ListAttachments(ctx context.Context, accountID, secretID uuid.UUID) ([]secret.SecretAttachment, error)
}

// WorkflowTemplateSource lists playbooks. These are the most laborious thing an
// account builds and the most valuable single part of this document.
type WorkflowTemplateSource interface {
	GetAllByAccountID(ctx context.Context, query workflow_template_list.WorkflowTemplateListQuery) ([]workflow_template.WorkflowTemplate, error)
}

// UtilityAssignmentSource lists utility assignments —
// which software roles the account declared, on which targets, with which
// params. Config carries no secret material by doctrine (params reference
// variable/attribute NAMES), so it exports verbatim.
type UtilityAssignmentSource interface {
	ListAssignments(ctx context.Context, accountID uuid.UUID) ([]utility.Assignment, error)
}

// ImageSource lists OS image records. The bytes are NOT in this document —
// they are gigabytes and have their own endpoint (GET /images/:id/download);
// what the export carries is the inventory, including each image's checksum, so
// a customer can tell whether they have taken all of them.
type ImageSource interface {
	GetAllByAccountID(ctx context.Context, query image_list.ImageListQuery) ([]image.Image, error)
}
