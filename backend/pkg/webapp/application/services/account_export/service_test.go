package account_export

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

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
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// The strings below are what must NEVER appear in an export document. Each one
// is planted in a different source, so a single scan of the marshalled JSON
// covers every leak path at once — which is the check that survives someone
// later adding a field without reading the package comment.
const (
	leakSecretValue     = "LEAK-secret-value"
	leakVariableValue   = "SAFE-variable-value"
	leakSSHPrivateKey   = "LEAK-ssh-private-key"
	leakAccessPassword  = "LEAK-access-password"
	leakOUPassword      = "LEAK-ou-default-password"
	leakPolicyPassword  = "LEAK-policy-default-password"
	leakNodePassword    = "LEAK-node-default-password"
	leakNodeToken       = "LEAK-node-unique-token"
	leakSSHKeyContentPk = "ssh-ed25519 AAAAPUBLIC"
)

type stubs struct {
	acct     *account.Account
	acctErr  error
	members  []member.Member
	ous      []organizational_unit.OrganizationalUnit
	ousErr   error
	policy   *organizational_unit.AccountPolicy
	nodes    []node.Node
	access   []access_entry.AccessEntry
	sshKeys  []ssh_key.SSHKey
	secrets  []secret.Secret
	secErr   error
	atts     []secret.SecretAttachment
	tmpls    []workflow_template.WorkflowTemplate
	images   []image.Image
	secCalls int
}

func (s *stubs) GetAccount(context.Context, uuid.UUID) (*account.Account, error) {
	return s.acct, s.acctErr
}
func (s *stubs) List(context.Context, commands.AuthContext) ([]member.Member, error) {
	return s.members, nil
}

// Separate adapter types, because four sources share the method name
// GetAllByAccountID with different signatures.
type ouAdapter struct{ s *stubs }

func (a ouAdapter) GetAllByAccountID(ctx context.Context, q organizational_unit_list.OrganizationalUnitListQuery) ([]organizational_unit.OrganizationalUnit, error) {
	return a.s.ous, a.s.ousErr
}
func (a ouAdapter) GetAccountPolicy(ctx context.Context, id uuid.UUID) (*organizational_unit.AccountPolicy, error) {
	return a.s.policy, nil
}

type nodeAdapter struct{ s *stubs }

func (a nodeAdapter) GetAllByAccountID(context.Context, node_list.NodeListQuery) ([]node.Node, error) {
	return a.s.nodes, nil
}

type accessAdapter struct{ s *stubs }

func (a accessAdapter) ListAllEntries(context.Context, uuid.UUID) ([]access_entry.AccessEntry, error) {
	return a.s.access, nil
}

type keyAdapter struct{ s *stubs }

func (a keyAdapter) GetAllByAccountID(context.Context, ssh_key_list.SSHKeyListQuery) ([]ssh_key.SSHKey, error) {
	return a.s.sshKeys, nil
}

type secretAdapter struct{ s *stubs }

func (a secretAdapter) List(_ context.Context, q secret_list.SecretListQuery) ([]secret.Secret, int64, error) {
	a.s.secCalls++
	if a.s.secErr != nil {
		return nil, 0, a.s.secErr
	}
	if q.Page > 1 {
		return nil, int64(len(a.s.secrets)), nil
	}
	return a.s.secrets, int64(len(a.s.secrets)), nil
}
func (a secretAdapter) ListAttachments(context.Context, uuid.UUID, uuid.UUID) ([]secret.SecretAttachment, error) {
	return a.s.atts, nil
}

type templateAdapter struct{ s *stubs }

func (a templateAdapter) GetAllByAccountID(context.Context, workflow_template_list.WorkflowTemplateListQuery) ([]workflow_template.WorkflowTemplate, error) {
	return a.s.tmpls, nil
}

type imageAdapter struct{ s *stubs }

func (a imageAdapter) GetAllByAccountID(context.Context, image_list.ImageListQuery) ([]image.Image, error) {
	return a.s.images, nil
}

func fullStubs() *stubs {
	ouPass := leakOUPassword
	ouUser := "deploy"
	polPass := leakPolicyPassword
	accessPass := leakAccessPassword
	yes := true
	return &stubs{
		acct: &account.Account{ID: uuid.New(), Name: "Acme"},
		members: []member.Member{{
			User:   &user.User{ID: uuid.New(), Email: "owner@acme.test", Name: "Owner", HashedPassword: "LEAK-password-hash"},
			Status: "active",
		}},
		ous: []organizational_unit.OrganizationalUnit{{
			ID: uuid.New(), Name: "Lab",
			NetworkFirst:    &yes,
			DefaultUsername: &ouUser,
			DefaultPassword: &ouPass,
		}},
		policy: &organizational_unit.AccountPolicy{DefaultPassword: &polPass},
		nodes: []node.Node{{
			ID: uuid.New(), Name: "node-1",
			UniqueToken:     leakNodeToken,
			DefaultPassword: leakNodePassword,
		}},
		access: []access_entry.AccessEntry{{
			ID: uuid.New(), Username: "ops", Sudo: true,
			Password: &accessPass,
		}},
		sshKeys: []ssh_key.SSHKey{{
			ID: uuid.New(), Alias: "deploy-key",
			KeyContent:    leakSSHKeyContentPk,
			PrivateKey:    leakSSHPrivateKey,
			HasPrivateKey: true,
		}},
		secrets: []secret.Secret{
			{ID: uuid.New(), Name: "api-token", Kind: secret.KindCredential, Sensitive: true, Value: leakSecretValue},
			{ID: uuid.New(), Name: "REGION", Key: "REGION", Kind: secret.KindEnv, Sensitive: false, Value: leakVariableValue},
		},
		tmpls:  []workflow_template.WorkflowTemplate{{ID: uuid.New(), Name: "Provision"}},
		images: []image.Image{{ID: uuid.New(), Title: "Ubuntu", Checksum: "sha256:abc"}},
	}
}

func build(t *testing.T, s *stubs) *Service {
	t.Helper()
	svc := NewService(slog.New(slog.DiscardHandler))
	return svc.WithSources(s, s, ouAdapter{s}, nodeAdapter{s}, accessAdapter{s},
		keyAdapter{s}, secretAdapter{s}, templateAdapter{s}, imageAdapter{s})
}

// THE test. Everything else in this file is detail; this is the promise the
// document makes on its own first page, and it is checked against the
// marshalled bytes rather than against struct fields, because a leak added by a
// future field would be invisible to a field-by-field assertion.
func TestExportCarriesNoCredentialMaterial(t *testing.T) {
	s := fullStubs()
	doc, err := build(t, s).Export(context.Background(), s.acct.ID, uuid.New())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)

	for _, forbidden := range []string{
		leakSecretValue, leakSSHPrivateKey, leakAccessPassword,
		leakOUPassword, leakPolicyPassword, leakNodePassword, leakNodeToken,
		"LEAK-password-hash", // a member's stored password hash
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the export leaks %q", forbidden)
		}
	}

	// The other half of the promise: what IS carried has to actually be there,
	// or the document is safe and useless.
	for _, required := range []string{
		"api-token",         // the secret NAME — the {{secrets.*}} identifier
		leakVariableValue,   // a non-sensitive variable's value IS exported
		leakSSHKeyContentPk, // the PUBLIC half of an SSH key
		`"ops"`, "deploy",   // usernames (JSON-quoted: a bare "ops" is a substring of ordinary words), so the config can be rebuilt
		"owner@acme.test", // members
		"Provision",       // playbooks
		"sha256:abc",      // image checksums, to verify a downloaded copy
	} {
		if !strings.Contains(body, required) {
			t.Errorf("the export is missing %q", required)
		}
	}
}

// The has_* booleans are how the document stays useful without carrying the
// value: "a credential exists here and must be re-entered" is the information
// a rebuild needs.
func TestExportReportsCredentialsExistWithoutCarryingThem(t *testing.T) {
	s := fullStubs()
	doc, _ := build(t, s).Export(context.Background(), s.acct.ID, uuid.New())

	if len(doc.OrganizationalUnits) != 1 || !doc.OrganizationalUnits[0].HasDefaultPassword {
		t.Error("an OU with a default password must report has_default_password")
	}
	if doc.AccountPolicy == nil || !doc.AccountPolicy.HasDefaultPassword {
		t.Error("the account policy's default password must be reported as present")
	}
	if len(doc.AccessEntries) != 1 || !doc.AccessEntries[0].HasPassword {
		t.Error("an access entry with a password must report has_password")
	}
	if len(doc.SSHKeys) != 1 || !doc.SSHKeys[0].HasPrivateHalf {
		t.Error("a platform-generated key must say the private half exists here")
	}
	// And a sensitive secret says it has a value while never showing it.
	var sensitive bool
	for _, sec := range doc.Secrets {
		if sec.Name == "api-token" {
			sensitive = sec.Sensitive && sec.HasValue && sec.Value == ""
		}
	}
	if !sensitive {
		t.Error("a sensitive secret must report has_value=true with an empty value")
	}
}

// Tri-state, not a bool: "inherit" and "explicitly off" are different states and
// a plain false loses the difference on the way back in.
func TestExportRendersInheritablePoliciesAsTriState(t *testing.T) {
	s := fullStubs()
	doc, _ := build(t, s).Export(context.Background(), s.acct.ID, uuid.New())
	ou := doc.OrganizationalUnits[0]
	if ou.NetworkFirst != string(organizational_unit.PolicyOn) {
		t.Errorf("network_first = %q, want %q", ou.NetworkFirst, organizational_unit.PolicyOn)
	}
	if ou.BootToOS != string(organizational_unit.PolicyInherit) {
		t.Errorf("boot_to_os = %q, want %q for an unset flag", ou.BootToOS, organizational_unit.PolicyInherit)
	}
}

// A section that cannot be read must be NAMED, not silently empty: an empty
// array and an unreadable one are the same JSON otherwise, and a customer would
// conclude they have no secrets.
func TestAFailedSectionIsNamedRatherThanSilentlyEmpty(t *testing.T) {
	s := fullStubs()
	s.secErr = errors.New("database is on fire")
	doc, err := build(t, s).Export(context.Background(), s.acct.ID, uuid.New())
	if err != nil {
		t.Fatalf("a failing section must not fail the whole export: %v", err)
	}
	if len(doc.Secrets) != 0 {
		t.Error("no secrets should have been collected")
	}
	var named bool
	for _, o := range doc.Omitted {
		if o.Section == "secrets" {
			named = true
		}
	}
	if !named {
		t.Fatal("the failed section is not named in `omitted`")
	}
	// The rest still has to be there — that is the point of continuing.
	if len(doc.Nodes) == 0 || len(doc.Playbooks) == 0 {
		t.Error("one failed section cost the others")
	}
	// And the reason must not carry the underlying error, which can quote a row.
	for _, o := range doc.Omitted {
		if strings.Contains(o.Reason, "on fire") {
			t.Error("the omission reason leaks the underlying error into a shared document")
		}
	}
}

// Losing the account read is the one failure that makes the rest meaningless.
func TestExportFailsWhenTheAccountItselfCannotBeRead(t *testing.T) {
	s := fullStubs()
	s.acctErr = errors.New("gone")
	if _, err := build(t, s).Export(context.Background(), s.acct.ID, uuid.New()); err == nil {
		t.Fatal("expected an error when the account cannot be read")
	}
}

// A nil source omits its section instead of panicking or failing the export.
func TestNilSourcesOmitTheirSections(t *testing.T) {
	s := fullStubs()
	svc := NewService(slog.New(slog.DiscardHandler)).WithSources(s, nil, nil, nil, nil, nil, nil, nil, nil)
	doc, err := svc.Export(context.Background(), s.acct.ID, uuid.New())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if doc.Account.Name != "Acme" {
		t.Error("the account section should still be present")
	}
	if len(doc.Nodes) != 0 || len(doc.Secrets) != 0 {
		t.Error("sections with no source must be empty")
	}
}

// The notice travels inside the document because the document travels: by the
// time someone opens the file they no longer have the endpoint's docs.
func TestTheDocumentExplainsItsOwnOmissions(t *testing.T) {
	s := fullStubs()
	doc, _ := build(t, s).Export(context.Background(), s.acct.ID, uuid.New())
	for _, want := range []string{"no secret values", "image", "download"} {
		if !strings.Contains(strings.ToLower(doc.Notice), strings.ToLower(want)) {
			t.Errorf("the notice never mentions %q", want)
		}
	}
	if doc.FormatVersion != FormatVersion {
		t.Errorf("format_version = %d, want %d", doc.FormatVersion, FormatVersion)
	}
	if len(doc.Images) != 1 || !strings.HasSuffix(doc.Images[0].DownloadPath, "/download") {
		t.Error("each image must name where its bytes are actually fetched from")
	}
}

// Paging must terminate on the first short page rather than spinning to the
// bound; the bound is a runaway guard, not the normal exit.
func TestSecretPagingStopsWhenTheTotalIsReached(t *testing.T) {
	s := fullStubs()
	build(t, s).Export(context.Background(), s.acct.ID, uuid.New())
	if s.secCalls != 1 {
		t.Errorf("secret list called %d times, want 1 for a single short page", s.secCalls)
	}
}
