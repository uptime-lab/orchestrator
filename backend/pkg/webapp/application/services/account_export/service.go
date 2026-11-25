package account_export

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/organizational_unit_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/secret_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/ssh_key_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/workflow_template_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// deref renders an optional inherited string as the empty string, which is what
// the document means by "not set at this level".
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// FormatVersion identifies the document shape. A consumer that reads this
// document years from now needs to know which fields it may rely on, and a
// bare timestamp does not say that.
//
// Bump it when a field is REMOVED or changes meaning. Adding a field does not
// need a bump: every reader of a JSON document tolerates unknown keys, and
// bumping for additions trains consumers to ignore the number.
const FormatVersion = 1

// secretPageSize walks the paginated secret list. The API caps a page at
// MaxPerPage; the export wants all of them, so it pages rather than asking for
// a number the query would silently clamp — a clamp here would truncate the
// document and look like the account simply has fewer secrets.
const secretPageSize = 200

// maxSecretPages bounds the walk. An account cannot legitimately hold 200k
// secrets, so hitting this means a repository that never advances, and looping
// forever inside an HTTP handler is worse than an incomplete document that says
// it is incomplete.
const maxSecretPages = 1000

// Service assembles the export document.
//
// Every source is optional. A nil source omits its section and records why in
// the document's Omitted list, rather than failing the whole export: an account
// that cannot read its playbooks should still get its nodes, and an export that
// refuses entirely is the one thing worse than a partial one for someone who is
// leaving.
type Service struct {
	accounts  AccountSource
	members   MemberSource
	ous       OrganizationalUnitSource
	nodes     NodeSource
	access    AccessSource
	sshKeys   SSHKeySource
	secrets   SecretSource
	playbooks WorkflowTemplateSource
	images    ImageSource
	utilities UtilityAssignmentSource
	logger    *slog.Logger
}

// SetUtilitySource wires the utilities section (added after WithSources'
// signature was fixed; a separate setter keeps existing call sites intact).
func (s *Service) SetUtilitySource(u UtilityAssignmentSource) { s.utilities = u }

// NewService wires the exporter. Only the logger is required — see the type
// comment for why the sources are not.
func NewService(logger *slog.Logger) *Service {
	if logger == nil {
		panic("account_export: logger is required")
	}
	return &Service{logger: logger.With("service", "account_export")}
}

// WithSources sets the sources. Separate from the constructor because the
// composition root builds these services in an order this one cannot dictate,
// which is the same setter pattern main.go already uses for the gRPC server.
func (s *Service) WithSources(
	accounts AccountSource,
	members MemberSource,
	ous OrganizationalUnitSource,
	nodes NodeSource,
	access AccessSource,
	sshKeys SSHKeySource,
	secrets SecretSource,
	playbooks WorkflowTemplateSource,
	images ImageSource,
) *Service {
	s.accounts = accounts
	s.members = members
	s.ous = ous
	s.nodes = nodes
	s.access = access
	s.sshKeys = sshKeys
	s.secrets = secrets
	s.playbooks = playbooks
	s.images = images
	return s
}

// Export builds the document for one account.
//
// It returns an error only when the ACCOUNT itself cannot be read — that is the
// one failure that makes the rest meaningless. Every other source failure is
// recorded in Omitted and the document is still produced, because a customer
// offboarding needs whatever can be given, and a document that names its own
// gaps is honest in a way a 500 is not.
func (s *Service) Export(ctx context.Context, accountID, actorUserID uuid.UUID) (*results.AccountExportResult, error) {
	if s.accounts == nil {
		return nil, fmt.Errorf("account export is not configured on this server")
	}
	acct, err := s.accounts.GetAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the account: %w", err)
	}

	auth := commands.NewAuthContext(accountID, actorUserID)
	doc := &results.AccountExportResult{
		FormatVersion: FormatVersion,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Account: results.AccountExportAccount{
			ID:               acct.ID,
			Name:             acct.Name,
			RequireTwoFactor: acct.RequireTwoFactor,
			CreatedAt:        acct.CreatedAt.UTC().Format(time.RFC3339),
		},
		Notice: exportNotice,
	}

	// Each section below is independent: one failing must not cost the others.
	s.addMembers(ctx, doc, auth)
	s.addOrganizationalUnits(ctx, doc, auth, accountID)
	s.addNodes(ctx, doc, auth)
	s.addAccess(ctx, doc, accountID)
	s.addSSHKeys(ctx, doc, auth)
	s.addSecrets(ctx, doc, auth, accountID)
	s.addPlaybooks(ctx, doc, auth)
	s.addImages(ctx, doc, auth)
	s.addUtilities(ctx, doc, accountID)

	return doc, nil
}

// exportNotice travels inside the document because the document travels. By the
// time someone opens this file they no longer have the endpoint's documentation,
// and the two things they will get wrong are assuming the secrets are in here
// and assuming the images are.
const exportNotice = "This export carries configuration and metadata only. It contains NO credential " +
	"material: no secret values, no SSH private keys, no access-entry passwords, no adoption or " +
	"master tokens. Secret and variable NAMES are included because they are what playbooks " +
	"reference; their values must be re-entered at the destination. OS image FILES are not " +
	"included either — this document lists them with their checksums, and each is downloaded " +
	"separately from GET /api/v1/images/{id}/download."

func (s *Service) omit(doc *results.AccountExportResult, section string, err error) {
	// The reason is the operator's, not the reader's: it names a section and a
	// failure, never a row. Logged with the request scope for correlation.
	doc.Omitted = append(doc.Omitted, results.AccountExportOmission{
		Section: section,
		Reason:  "this section could not be read; the rest of the export is unaffected",
	})
	s.logger.ErrorContext(context.Background(), "account export section failed", "section", section, "error", err)
}

func (s *Service) addMembers(ctx context.Context, doc *results.AccountExportResult, auth commands.AuthContext) {
	if s.members == nil {
		return
	}
	list, err := s.members.List(ctx, auth)
	if err != nil {
		s.omit(doc, "members", err)
		return
	}
	for i := range list {
		m := list[i]
		if m.User == nil {
			continue
		}
		entry := results.AccountExportMember{
			UserID: m.User.ID,
			Email:  m.User.Email,
			Name:   m.User.Name,
			Status: string(m.Status),
		}
		for _, b := range m.Bindings {
			binding := results.AccountExportBinding{Role: string(b.Role)}
			if b.OUID != nil {
				id := b.OUID.String()
				binding.OrganizationalUnitID = &id
			}
			entry.Bindings = append(entry.Bindings, binding)
		}
		doc.Members = append(doc.Members, entry)
	}
}

func (s *Service) addOrganizationalUnits(ctx context.Context, doc *results.AccountExportResult, auth commands.AuthContext, accountID uuid.UUID) {
	if s.ous == nil {
		return
	}
	units, err := s.ous.GetAllByAccountID(ctx, organizational_unit_list.OrganizationalUnitListQuery{AuthContext: auth})
	if err != nil {
		s.omit(doc, "organizational_units", err)
	} else {
		for i := range units {
			u := units[i]
			entry := results.AccountExportOU{
				ID:       u.ID,
				Name:     u.Name,
				Position: u.Position,
			}
			if u.ParentID != nil {
				p := u.ParentID.String()
				entry.ParentID = &p
			}
			// Provisioning defaults are exported WITHOUT default_password: it is
			// a credential, and on many accounts it is THE fleet credential.
			entry.NetworkFirst = string(organizational_unit.PolicyFlagFromBool(u.NetworkFirst))
			entry.BootToOS = string(organizational_unit.PolicyFlagFromBool(u.BootToOS))
			entry.SSHPasswordAuth = string(organizational_unit.PolicyFlagFromBool(u.SSHPasswordAuth))
			entry.DefaultUsername = deref(u.DefaultUsername)
			entry.HasDefaultPassword = u.DefaultPassword != nil && *u.DefaultPassword != ""
			if u.SSHKeyID != nil {
				k := u.SSHKeyID.String()
				entry.SSHKeyID = &k
			}
			if u.OSImageID != nil {
				img := u.OSImageID.String()
				entry.OSImageID = &img
			}
			doc.OrganizationalUnits = append(doc.OrganizationalUnits, entry)
		}
	}

	policy, err := s.ous.GetAccountPolicy(ctx, accountID)
	if err != nil {
		s.omit(doc, "account_policy", err)
		return
	}
	if policy != nil {
		doc.AccountPolicy = &results.AccountExportPolicy{
			NetworkFirst:       string(organizational_unit.PolicyFlagFromBool(policy.NetworkFirst)),
			BootToOS:           string(organizational_unit.PolicyFlagFromBool(policy.BootToOS)),
			SSHPasswordAuth:    string(organizational_unit.PolicyFlagFromBool(policy.SSHPasswordAuth)),
			DefaultUsername:    deref(policy.DefaultUsername),
			HasDefaultPassword: policy.DefaultPassword != nil && *policy.DefaultPassword != "",
		}
		if policy.SSHKeyID != nil {
			k := policy.SSHKeyID.String()
			doc.AccountPolicy.SSHKeyID = &k
		}
		if policy.OSImageID != nil {
			img := policy.OSImageID.String()
			doc.AccountPolicy.OSImageID = &img
		}
	}
}

func (s *Service) addNodes(ctx context.Context, doc *results.AccountExportResult, auth commands.AuthContext) {
	if s.nodes == nil {
		return
	}
	nodes, err := s.nodes.GetAllByAccountID(ctx, node_list.NodeListQuery{AuthContext: auth})
	if err != nil {
		s.omit(doc, "nodes", err)
		return
	}
	for i := range nodes {
		n := nodes[i]
		// UniqueToken is the device's credential for this account and is the
		// single most dangerous field in the node record. It is not exported,
		// and a node's identity in this document is its serial — which is
		// stamped on the hardware the customer is keeping.
		entry := results.AccountExportNode{
			ID:           n.ID,
			Name:         n.Name,
			Hostname:     n.Hostname,
			SerialNumber: n.GetHardwareSerial(),
			Status:       string(n.Status),
			AgentVersion: n.AgentVersion,
			AgentKind:    n.AgentKind,
			IPv4:         n.IPv4,
			MACAddress:   n.MacAddress,
			SoC:          n.SoC(),
			NetworkFirst: string(organizational_unit.PolicyFlagFromBool(n.NetworkFirst)),
			BootToOS:     string(organizational_unit.PolicyFlagFromBool(n.BootToOS)),
			Notes:        n.Notes,
		}
		if n.OUID != nil {
			ou := n.OUID.String()
			entry.OrganizationalUnitID = &ou
		}
		doc.Nodes = append(doc.Nodes, entry)
	}
}

func (s *Service) addAccess(ctx context.Context, doc *results.AccountExportResult, accountID uuid.UUID) {
	if s.access == nil {
		return
	}
	entries, err := s.access.ListAllEntries(ctx, accountID)
	if err != nil {
		s.omit(doc, "access_entries", err)
		return
	}
	for i := range entries {
		e := entries[i]
		out := results.AccountExportAccessEntry{
			ID:       e.ID,
			Kind:     string(e.Type),
			Username: e.Username,
			Sudo:     e.Sudo,
			// The password is encrypted at rest and reaches devices only as a
			// crypt(3) hash. Neither form belongs in a portable file, so the
			// export says whether one exists and stops there.
			HasPassword: e.Password != nil && *e.Password != "",
			Principals:  e.Principals,
			AccountWide: e.AccountWide,
		}
		for _, id := range e.KeyIDs {
			out.SSHKeyIDs = append(out.SSHKeyIDs, id.String())
		}
		for _, id := range e.OUIDs {
			out.OrganizationalUnitIDs = append(out.OrganizationalUnitIDs, id.String())
		}
		if e.NodeID != nil {
			n := e.NodeID.String()
			out.NodeID = &n
		}
		doc.AccessEntries = append(doc.AccessEntries, out)
	}
}

func (s *Service) addSSHKeys(ctx context.Context, doc *results.AccountExportResult, auth commands.AuthContext) {
	if s.sshKeys == nil {
		return
	}
	keys, err := s.sshKeys.GetAllByAccountID(ctx, ssh_key_list.SSHKeyListQuery{AuthContext: auth})
	if err != nil {
		s.omit(doc, "ssh_keys", err)
		return
	}
	for i := range keys {
		k := keys[i]
		// PUBLIC half only. A generated key's private half is encrypted at rest
		// under SECRETS_ENCRYPTION_KEY; putting it in a portable document would
		// place fleet-wide login material in a file no key rotation can reach.
		doc.SSHKeys = append(doc.SSHKeys, results.AccountExportSSHKey{
			ID:             k.ID,
			Alias:          k.Alias,
			KeyType:        string(k.KeyType),
			PublicKey:      k.KeyContent,
			Fingerprint:    k.Fingerprint,
			IsDefault:      k.IsDefault,
			Tags:           k.Tags,
			HasPrivateHalf: k.HasPrivateKey,
		})
	}
}

func (s *Service) addSecrets(ctx context.Context, doc *results.AccountExportResult, auth commands.AuthContext, accountID uuid.UUID) {
	if s.secrets == nil {
		return
	}
	for page := 1; page <= maxSecretPages; page++ {
		batch, total, err := s.secrets.List(ctx, secret_list.SecretListQuery{
			AuthContext: auth,
			Page:        page,
			PerPage:     secretPageSize,
			Sort:        "name",
			Order:       "asc",
		})
		if err != nil {
			s.omit(doc, "secrets", err)
			return
		}
		if len(batch) == 0 {
			return
		}
		for i := range batch {
			sec := batch[i]
			out := results.AccountExportSecret{
				ID:        sec.ID,
				Name:      sec.Name,
				Key:       sec.Key,
				Kind:      string(sec.Kind),
				Sensitive: sec.Sensitive,
				Tags:      sec.Tags,
			}
			// The value is exported for a non-sensitive VARIABLE and never for a
			// secret. That is not a new policy invented here: the API itself
			// returns `value` for one and `has_value` for the other, and the
			// sensitive→non-sensitive transition is forbidden precisely so this
			// distinction cannot be flipped after the fact.
			if !sec.Sensitive && sec.Kind == secret.KindEnv {
				out.Value = sec.Value
			}
			out.HasValue = sec.HasValue()

			if s.secrets != nil {
				attachments, aerr := s.secrets.ListAttachments(ctx, accountID, sec.ID)
				if aerr != nil {
					// One secret's attachments failing must not drop the secret
					// itself; the section note says the set may be incomplete.
					s.omit(doc, "secret_attachments", aerr)
				} else {
					for _, a := range attachments {
						att := results.AccountExportSecretAttachment{Tag: a.Tag}
						if a.OUID != nil {
							ou := a.OUID.String()
							att.OrganizationalUnitID = &ou
						}
						if a.NodeID != nil {
							n := a.NodeID.String()
							att.NodeID = &n
						}
						out.Attachments = append(out.Attachments, att)
					}
				}
			}
			doc.Secrets = append(doc.Secrets, out)
		}
		if int64(len(doc.Secrets)) >= total {
			return
		}
	}
	s.omit(doc, "secrets", fmt.Errorf("paging did not terminate after %d pages", maxSecretPages))
}

func (s *Service) addPlaybooks(ctx context.Context, doc *results.AccountExportResult, auth commands.AuthContext) {
	if s.playbooks == nil {
		return
	}
	templates, err := s.playbooks.GetAllByAccountID(ctx, workflow_template_list.WorkflowTemplateListQuery{AuthContext: auth})
	if err != nil {
		s.omit(doc, "playbooks", err)
		return
	}
	for i := range templates {
		t := templates[i]
		// The graph is exported verbatim: it is the thing that took work to
		// build, and its `{{secrets.*}}` references are NAMES, not values.
		entry := results.AccountExportPlaybook{
			ID:          t.ID,
			Name:        t.Name,
			Description: t.Description,
			Status:      string(t.Status),
			IsShortcut:  t.IsShortcut,
			Graph:       t.Graph,
		}
		// The set is the truth; the singular field stays populated for a
		// one-unit playbook so an older reader of an export still sees a scope.
		for _, ouID := range t.OUIDs {
			entry.OrganizationalUnitIDs = append(entry.OrganizationalUnitIDs, ouID.String())
		}
		if single := t.PrimaryOUID(); single != nil {
			ou := single.String()
			entry.OrganizationalUnitID = &ou
		}
		doc.Playbooks = append(doc.Playbooks, entry)
	}
}

func (s *Service) addUtilities(ctx context.Context, doc *results.AccountExportResult, accountID uuid.UUID) {
	if s.utilities == nil {
		return
	}
	assignments, err := s.utilities.ListAssignments(ctx, accountID)
	if err != nil {
		s.omit(doc, "utilities", err)
		return
	}
	for i := range assignments {
		a := assignments[i]
		entry := results.AccountExportUtilityAssignment{
			ID:          a.ID,
			UtilityID:   a.UtilityID,
			Scope:       string(a.Scope()),
			AccountWide: a.AccountWide,
			Config:      a.Config,
			Enabled:     a.Enabled,
		}
		for _, ouID := range a.OUIDs {
			entry.OrganizationalUnitIDs = append(entry.OrganizationalUnitIDs, ouID.String())
		}
		// The set is the truth; the singular field stays populated for a
		// one-unit assignment so an older reader still sees a scope.
		if len(a.OUIDs) > 0 {
			ou := a.OUIDs[0].String()
			entry.OrganizationalUnitID = &ou
		}
		if a.NodeID != nil {
			n := a.NodeID.String()
			entry.NodeID = &n
		}
		doc.Utilities = append(doc.Utilities, entry)
	}
}

func (s *Service) addImages(ctx context.Context, doc *results.AccountExportResult, auth commands.AuthContext) {
	if s.images == nil {
		return
	}
	images, err := s.images.GetAllByAccountID(ctx, image_list.ImageListQuery{AuthContext: auth})
	if err != nil {
		s.omit(doc, "images", err)
		return
	}
	for i := range images {
		img := images[i]
		entry := results.AccountExportImage{
			ID:          img.ID,
			Title:       img.Title,
			Version:     img.Version,
			Description: img.Description,
			Kind:        img.EffectiveKind(),
			Checksum:    img.Checksum,
			// DownloadPath is spelled out because by the time someone reads this
			// file they no longer have the API documentation, and "the images
			// are missing" is the wrong conclusion to reach.
			DownloadPath: fmt.Sprintf("/api/v1/images/%s/download", img.ID),
		}
		if img.FileStorageInfo != nil {
			entry.FileName = img.FileStorageInfo.FileName
			entry.SizeBytes = img.FileStorageInfo.SizeBytes
		}
		doc.Images = append(doc.Images, entry)
	}
}
