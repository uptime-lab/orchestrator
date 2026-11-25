package organizational_unit

import (
	"context"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/organizational_unit_summary"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// ouSummarySampleLimit bounds every section's item sample. The section carries
// the true total; the page links to the original list for the rest.
const ouSummarySampleLimit = 5

// SummarySections says which sections the CALLER may read. The handler decides
// it per action against the target unit; the service fills only what is
// allowed and names the rest in Omitted, so an empty section and a forbidden
// one are different JSON (the account-export rule).
type SummarySections struct {
	Nodes        bool
	Access       bool
	Secrets      bool
	Workflows    bool
	MasterTokens bool
}

// Summary builds the Organization page's one-call overview of a unit. Section
// reads are best-effort: a failing section is logged and named in Omitted
// rather than failing the unit view — only the unit read itself is fatal.
func (s *Service) Summary(ctx context.Context, q organizational_unit_summary.OrganizationalUnitSummaryQuery, include SummarySections) (*results.OrganizationalUnitSummaryResult, error) {
	accountID := q.AuthContext.AccountID
	unit, err := s.ouRepo.GetByID(ctx, accountID, q.ID)
	if err != nil {
		return nil, err
	}

	// Parent (nil for a root; a dangling parent degrades to nil, the same rule
	// loadOUChain applies for nodes) and account policy feed the resolver.
	var parent *organizational_unit.OrganizationalUnit
	if unit.ParentID != nil {
		p, err := s.ouRepo.GetByID(ctx, accountID, *unit.ParentID)
		if err == nil {
			parent = p
		} else {
			s.logger.WarnContext(ctx, "unit has a dangling parent reference; resolving without it",
				"ou_id", unit.ID, "parent_id", *unit.ParentID, "error", err)
		}
	}
	accountPolicy, err := s.GetAccountPolicy(ctx, accountID)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to load account policy for summary", "error", err)
		accountPolicy = nil
	}

	out := &results.OrganizationalUnitSummaryResult{
		Unit: results.NewOrganizationalUnitResult(unit),
		// Node level empty by construction: this is the UNIT's chain.
		EffectivePolicy: organizational_unit.ResolveEffectivePolicy(
			organizational_unit.NodePolicyInputs{}, unit, parent, accountPolicy),
	}
	if eq, source, ok := organizational_unit.ResolveDeviceProfile(nil, unit, parent, accountPolicy); ok {
		out.EffectiveDeviceProfile = &eq
		out.EffectiveDeviceProfileSource = source
	}
	s.fillEffectiveNames(ctx, accountID, out)

	children := s.childUnits(ctx, accountID, unit.ID)
	out.Counts.Children = len(children)
	out.PasswordAuthImpact = len(s.nodesResolvingPasswordAuthAt(ctx, accountID, &unit.ID))

	omit := func(section string) { out.Omitted = append(out.Omitted, section) }

	if include.Nodes {
		if section, childNodes, err := s.summaryNodes(ctx, accountID, unit.ID, children); err == nil {
			out.Nodes = section
			out.Counts.Nodes = section.Total - childNodes
			out.Counts.ChildNodes = childNodes
		} else {
			s.logger.WarnContext(ctx, "summary nodes section failed", "error", err, "ou_id", unit.ID)
			omit("nodes")
		}
	} else {
		omit("nodes")
	}

	if include.Access && s.accessEntryRepo != nil {
		if section, err := s.summaryAccess(ctx, accountID, unit); err == nil {
			out.AccessEntries = section
			out.Counts.AccessEntries = section.Total
		} else {
			s.logger.WarnContext(ctx, "summary access section failed", "error", err, "ou_id", unit.ID)
			omit("access_entries")
		}
	} else {
		omit("access_entries")
	}

	if include.Secrets {
		if section, err := s.summarySecrets(ctx, accountID, unit.ID); err == nil {
			out.Secrets = section
			out.Counts.Secrets = section.Total
		} else {
			s.logger.WarnContext(ctx, "summary secrets section failed", "error", err, "ou_id", unit.ID)
			omit("secrets")
		}
	} else {
		omit("secrets")
	}

	if include.Workflows {
		if section, err := s.summaryWorkflows(ctx, accountID, unit.ID); err == nil {
			out.Workflows = section
			out.Counts.Workflows = section.Total
		} else {
			s.logger.WarnContext(ctx, "summary workflows section failed", "error", err, "ou_id", unit.ID)
			omit("workflows")
		}
	} else {
		omit("workflows")
	}

	if include.MasterTokens {
		if section, err := s.summaryMasterTokens(ctx, accountID, unit.ID); err == nil {
			out.MasterTokens = section
			out.Counts.MasterTokens = section.Total
		} else {
			s.logger.WarnContext(ctx, "summary master tokens section failed", "error", err, "ou_id", unit.ID)
			omit("master_tokens")
		}
	} else {
		omit("master_tokens")
	}

	return out, nil
}

// fillEffectiveNames resolves display names for the effective SSH key and OS
// image references. Best-effort: a dangling reference leaves the name empty,
// exactly like the provisioning resolve path degrades it.
func (s *Service) fillEffectiveNames(ctx context.Context, accountID uuid.UUID, out *results.OrganizationalUnitSummaryResult) {
	if id := out.EffectivePolicy.SSHKeyID.Value; id != nil && s.sshKeyRepo != nil {
		if key, err := s.sshKeyRepo.GetByID(ctx, accountID, *id); err == nil {
			out.EffectiveSSHKeyName = key.Alias
		}
	}
	if id := out.EffectivePolicy.OSImageID.Value; id != nil && s.imageRepo != nil {
		if img, err := s.imageRepo.GetByID(ctx, accountID, *id); err == nil {
			out.EffectiveOSImageName = img.Title
		}
	}
}

// childUnits lists the direct children of a unit (2-level tree: one hop).
func (s *Service) childUnits(ctx context.Context, accountID, ouID uuid.UUID) []organizational_unit.OrganizationalUnit {
	all, err := s.ouRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to list units for summary", "error", err, "ou_id", ouID)
		return nil
	}
	children := make([]organizational_unit.OrganizationalUnit, 0)
	for i := range all {
		if all[i].ParentID != nil && *all[i].ParentID == ouID {
			children = append(children, all[i])
		}
	}
	return children
}

// summaryNodes samples the devices the unit's policies reach: direct members
// plus (for a root) child-unit members, each with its policy-inheritance mode.
func (s *Service) summaryNodes(ctx context.Context, accountID, ouID uuid.UUID, children []organizational_unit.OrganizationalUnit) (*results.OUSummaryNodesSection, int, error) {
	nodes, err := s.nodeRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		return nil, 0, err
	}
	childByID := make(map[uuid.UUID]*organizational_unit.OrganizationalUnit, len(children))
	for i := range children {
		childByID[children[i].ID] = &children[i]
	}

	section := &results.OUSummaryNodesSection{Items: make([]results.OUSummaryNodeResult, 0, ouSummarySampleLimit)}
	childNodes := 0
	for i := range nodes {
		n := &nodes[i]
		var viaChild *organizational_unit.OrganizationalUnit
		switch {
		case n.OUID != nil && *n.OUID == ouID:
		case n.OUID != nil && childByID[*n.OUID] != nil:
			viaChild = childByID[*n.OUID]
			childNodes++
		default:
			continue
		}
		section.Total++
		if len(section.Items) >= ouSummarySampleLimit {
			continue
		}
		item := results.OUSummaryNodeResult{
			ID:     n.ID,
			Name:   n.Name,
			Status: string(n.Status),
		}
		overridden := nodeOverriddenPolicyFields(n)
		switch {
		case len(overridden) > 0:
			item.PolicyMode = "overridden"
			item.OverriddenFields = overridden
		case viaChild != nil && unitDefinesAnyPolicy(viaChild):
			item.PolicyMode = "via_child"
		default:
			item.PolicyMode = "inherits"
		}
		if viaChild != nil {
			id := viaChild.ID
			item.ViaChildOU = &id
		}
		section.Items = append(section.Items, item)
	}
	return section, childNodes, nil
}

// nodeOverriddenPolicyFields names the policy fields the node answers itself.
func nodeOverriddenPolicyFields(n *node.Node) []string {
	fields := make([]string, 0, 8)
	add := func(name string, set bool) {
		if set {
			fields = append(fields, name)
		}
	}
	add("network_first", n.NetworkFirst != nil)
	add("boot_to_os", n.BootToOS != nil)
	add("ssh_password_auth", n.SSHPasswordAuth != nil)
	add("default_username", n.DefaultUsername != "")
	add("default_password", n.DefaultPassword != "")
	add("ssh_key", n.SSHKeyID != nil)
	add("os_image", n.OSImageID != nil)
	add("device_profile", n.DeviceProfile != nil)
	add("led_config", n.LEDConfig != nil)
	return fields
}

// unitDefinesAnyPolicy reports whether the unit answers any policy field
// itself (vs passing everything through).
func unitDefinesAnyPolicy(ou *organizational_unit.OrganizationalUnit) bool {
	return ou.NetworkFirst != nil || ou.BootToOS != nil || ou.SSHPasswordAuth != nil ||
		(ou.DefaultUsername != nil && *ou.DefaultUsername != "") ||
		(ou.DefaultPassword != nil && *ou.DefaultPassword != "") ||
		ou.SSHKeyID != nil || ou.OSImageID != nil || ou.DeviceProfile != nil
}

// summaryAccess samples the access entries reaching this unit — from the unit
// itself, its parent, or account-wide — with the resolution's shadowing rule
// applied: the nearest level owning an OS username overrides it entirely.
func (s *Service) summaryAccess(ctx context.Context, accountID uuid.UUID, unit *organizational_unit.OrganizationalUnit) (*results.OUSummaryAccessSection, error) {
	levelOUs := []uuid.UUID{unit.ID}
	if unit.ParentID != nil {
		levelOUs = append(levelOUs, *unit.ParentID)
	}
	scoped, err := s.accessEntryRepo.ListByOUs(ctx, accountID, levelOUs)
	if err != nil {
		return nil, err
	}
	accountWide, err := s.accessEntryRepo.ListAccountLevel(ctx, accountID)
	if err != nil {
		return nil, err
	}

	type row struct {
		item results.OUSummaryAccessEntryResult
		rank int // 0 = unit, 1 = parent, 2 = account — nearest wins
	}
	rows := make([]row, 0, len(scoped)+len(accountWide))
	appendEntry := func(e results.OUSummaryAccessEntryResult, rank int) {
		e.EffectiveFrom = [...]string{"unit", "parent", "account"}[rank]
		rows = append(rows, row{item: e, rank: rank})
	}
	for i := range scoped {
		e := &scoped[i]
		rank := 1
		for _, id := range e.OUIDs {
			if id == unit.ID {
				rank = 0
				break
			}
		}
		appendEntry(summaryAccessItem(e), rank)
	}
	for i := range accountWide {
		appendEntry(summaryAccessItem(&accountWide[i]), 2)
	}

	// Shadowing: within one OS username, the nearest level wins entirely. An
	// entry spanning both the unit and its parent enters once, at rank 0, so
	// it never shadows itself (the EntryID guard of the resolver, by
	// construction here).
	nearest := make(map[string]int)
	for _, r := range rows {
		if r.item.Type != "user" || r.item.Username == "" {
			continue
		}
		if cur, ok := nearest[r.item.Username]; !ok || r.rank < cur {
			nearest[r.item.Username] = r.rank
		}
	}
	section := &results.OUSummaryAccessSection{Total: len(rows), Items: make([]results.OUSummaryAccessEntryResult, 0, ouSummarySampleLimit)}
	for _, r := range rows {
		if len(section.Items) >= ouSummarySampleLimit {
			break
		}
		item := r.item
		if item.Type == "user" && item.Username != "" && nearest[item.Username] < r.rank {
			item.Shadowed = true
		}
		section.Items = append(section.Items, item)
	}
	return section, nil
}

func summaryAccessItem(e *access_entry.AccessEntry) results.OUSummaryAccessEntryResult {
	return results.OUSummaryAccessEntryResult{
		ID:       e.ID,
		Type:     string(e.Type),
		Username: e.Username,
		KeyCount: len(e.KeyIDs),
		Sudo:     e.Sudo,
	}
}

// summarySecrets samples the unit's effective secret set with provenance.
// Metadata only: names, kinds and sources — the values never enter the DTO.
func (s *Service) summarySecrets(ctx context.Context, accountID, ouID uuid.UUID) (*results.OUSummarySecretsSection, error) {
	resolved, err := s.ResolveEffectiveSecretsForOU(ctx, accountID, ouID)
	if err != nil {
		return nil, err
	}
	section := &results.OUSummarySecretsSection{Total: len(resolved), Items: make([]results.OUSummarySecretResult, 0, ouSummarySampleLimit)}
	for i := range resolved {
		if len(section.Items) >= ouSummarySampleLimit {
			break
		}
		r := &resolved[i]
		section.Items = append(section.Items, results.OUSummarySecretResult{
			ID:          r.ID,
			Name:        r.Name,
			Kind:        r.Kind,
			Sensitive:   r.Sensitive,
			SourceLevel: r.Source.Level,
			Via:         r.Source.Via,
			Tag:         r.Source.Tag,
		})
	}
	return section, nil
}

// summaryWorkflows samples the playbooks whose OU scope names this unit.
func (s *Service) summaryWorkflows(ctx context.Context, accountID, ouID uuid.UUID) (*results.OUSummaryWorkflowsSection, error) {
	templates, err := s.templateRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	section := &results.OUSummaryWorkflowsSection{Items: make([]results.OUSummaryWorkflowResult, 0, ouSummarySampleLimit)}
	for i := range templates {
		t := &templates[i]
		inScope := false
		for _, id := range t.OUIDs {
			if id == ouID {
				inScope = true
				break
			}
		}
		if !inScope {
			continue
		}
		section.Total++
		if len(section.Items) >= ouSummarySampleLimit {
			continue
		}
		section.Items = append(section.Items, results.OUSummaryWorkflowResult{
			ID:           t.ID,
			Name:         t.Name,
			Status:       string(t.Status),
			ScopeOUCount: len(t.OUIDs),
		})
	}
	return section, nil
}

// summaryMasterTokens samples the adoption tokens whose default OU is this
// unit. Names and ids only — never token material.
func (s *Service) summaryMasterTokens(ctx context.Context, accountID, ouID uuid.UUID) (*results.OUSummaryMasterTokensSection, error) {
	tokens, err := s.masterTokenRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	section := &results.OUSummaryMasterTokensSection{Items: make([]results.OUSummaryMasterTokenResult, 0, ouSummarySampleLimit)}
	for i := range tokens {
		t := &tokens[i]
		if t.OUID == nil || *t.OUID != ouID {
			continue
		}
		section.Total++
		if len(section.Items) >= ouSummarySampleLimit {
			continue
		}
		section.Items = append(section.Items, results.OUSummaryMasterTokenResult{ID: t.ID, Name: t.Name})
	}
	return section, nil
}
