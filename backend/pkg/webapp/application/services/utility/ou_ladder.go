package utility

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

// OUUtilityLadder is one utility's rollup across a unit's nodes.
type OUUtilityLadder struct {
	UtilityID string
	Name      string
	// Nodes is how many of the unit's nodes this utility resolves to. It is NOT
	// the unit's node count: a nearer assignment on a node can override the
	// unit's, and a node in a child unit can hold a different set.
	Nodes int
	// The four rungs, summing to Nodes. Declared covers a node that has never
	// applied — including one whose ladder was forgotten after a reimage.
	Applied       int
	UpdatePending int
	Failed        int
	Declared      int
}

// LadderForOU rolls the per-node ladder up to one unit, for the unit page.
//
// Two shapes were possible and this is the cheaper one. Calling ResolveForNode
// per node would re-read every assignment and every unit once per node — an
// O(nodes) query storm behind one page load. Instead the assignments, the unit
// tree and the nodes are read ONCE and resolution runs in memory through the
// same pure domain function the per-node path uses, so the two cannot disagree
// about what reaches a node. The stored rungs come back in one query
// (ListByNodes).
//
// Hashes are NOT re-resolved here. The per-node view recomputes them with
// {{secrets.*}} substituted, which costs a secret resolution per node; a
// rollup that only counts rungs does not need it, and the stored DesiredHash is
// what the rung was derived against anyway.
//
// A unit that reaches no nodes returns an empty list, not an error: an empty
// unit is a normal state and the page renders it as one.
func (s *Service) LadderForOU(ctx context.Context, accountID, ouID uuid.UUID) ([]OUUtilityLadder, error) {
	// The reached set is the unit plus its children — the same expansion
	// targetNodes does, and for the same reason: the inheritance chain REACHES
	// a child unit's nodes even when a nearer level decides what they get.
	allOUs, err := s.ouRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("could not enumerate organizational units: %w", err)
	}
	reached := map[uuid.UUID]bool{ouID: true}
	for _, ou := range allOUs {
		if ou.ParentID != nil && *ou.ParentID == ouID {
			reached[ou.ID] = true
		}
	}
	parentOf := make(map[uuid.UUID]*uuid.UUID, len(allOUs))
	for _, ou := range allOUs {
		parentOf[ou.ID] = ou.ParentID
	}

	allNodes, err := s.nodeRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	nodeIDs := make([]uuid.UUID, 0)
	nodeOU := make(map[uuid.UUID]uuid.UUID)
	for _, n := range allNodes {
		if n.OUID != nil && reached[*n.OUID] {
			nodeIDs = append(nodeIDs, n.ID)
			nodeOU[n.ID] = *n.OUID
		}
	}
	if len(nodeIDs) == 0 {
		return nil, nil
	}

	assignments, err := s.assignments.ListByAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	states, err := s.states.ListByNodes(ctx, accountID, nodeIDs)
	if err != nil {
		return nil, err
	}
	stateByNode := make(map[uuid.UUID]map[string]utility.NodeState, len(nodeIDs))
	for _, st := range states {
		if stateByNode[st.NodeID] == nil {
			stateByNode[st.NodeID] = map[string]utility.NodeState{}
		}
		stateByNode[st.NodeID][st.UtilityID] = st
	}

	rollup := map[string]*OUUtilityLadder{}
	for _, nodeID := range nodeIDs {
		chain := ouChainFrom(nodeOU[nodeID], parentOf)
		for _, r := range utility.ResolveForNode(s.catalog, assignments, nodeID, chain) {
			entry := rollup[r.Definition.ID]
			if entry == nil {
				entry = &OUUtilityLadder{UtilityID: r.Definition.ID, Name: r.Definition.Name}
				rollup[r.Definition.ID] = entry
			}
			entry.Nodes++
			st, ok := stateByNode[nodeID][r.Definition.ID]
			if !ok {
				// No row at all is the same fact as a row that was never
				// applied, and the per-node view renders it the same way.
				entry.Declared++
				continue
			}
			status := st.Status()
			// The stored rungs describe the world AS OF THE LAST APPLY; an
			// assignment edited since then must not keep reading applied
			// (a stale green). r.ConfigHash here is the current
			// PRE-resolution hash — comparing it against the source hash the
			// apply stored catches every config and scope edit without
			// resolving secrets per node (resolution stamps last_used_at, and
			// a page load faking secret usage is worse than the staleness).
			// A secret VALUE edit is the residual blind spot, deliberately:
			// the per-node view resolves fresh and stays the authority there.
			// Legacy rows (empty source hash) keep the stored rung until their
			// next apply fills the column.
			if status == utility.StatusApplied &&
				st.DesiredSourceHash != "" && st.DesiredSourceHash != r.ConfigHash {
				status = utility.StatusUpdatePending
			}
			switch status {
			case utility.StatusApplied:
				entry.Applied++
			case utility.StatusUpdatePending:
				entry.UpdatePending++
			case utility.StatusFailed:
				entry.Failed++
			default:
				entry.Declared++
			}
		}
	}

	out := make([]OUUtilityLadder, 0, len(rollup))
	for _, entry := range rollup {
		out = append(out, *entry)
	}
	// By utility id, so the section is stable across reloads.
	sort.Slice(out, func(i, j int) bool { return out[i].UtilityID < out[j].UtilityID })
	return out, nil
}

// ouChainFrom builds a node's unit lineage nearest-first, which is what
// ResolveForNode compares distances against. The tree is 2-level, so this is at
// most two entries; the loop is bounded anyway, because a corrupted parent
// pointer must not hang a page load.
func ouChainFrom(ouID uuid.UUID, parentOf map[uuid.UUID]*uuid.UUID) []uuid.UUID {
	chain := []uuid.UUID{ouID}
	seen := map[uuid.UUID]bool{ouID: true}
	for cur := ouID; ; {
		parent, ok := parentOf[cur]
		if !ok || parent == nil || seen[*parent] {
			return chain
		}
		chain = append(chain, *parent)
		seen[*parent] = true
		cur = *parent
	}
}
