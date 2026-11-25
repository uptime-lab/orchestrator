package utility

import (
	"context"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

// Phase 2 of the Utilities design (docs/UTILITIES-PLAN.md §3.5): the reconcile
// loop. Without it an assignment would reach a node only when somebody
// pressed Apply, which contradicts the promise the feature is sold on —
// "continuously reconciled, future joiners included".
//
// The trigger is the same one access config self-heals on: a managed-OS
// system-info report, the only moment the node's real boot mode is fresh.
// That is NOT a frequent tick — the agent reports system info on connect, on
// reconnect, and after a command that changes hardware state — so the steady
// state costs two cheap reads per report, not a poll. That
// covers all three phase-2 cases with one path — a node joining an OU that
// already has assignments (no ladder rows), a config edited while the node was
// away (the source hash moved), and a reimaged node (install_os forgets the
// ladder, so the rows are gone and this reads as first-connect).
//
// Everything below is best-effort: reconciliation must never fail a
// system-info update, and "we do not know" is never grounds for dispatching.

// utilityDriftMaxConsecutiveFailures is how many times the loop re-dispatches
// ONE desired config a node keeps failing before it stands down — the
// access-drift precedent (accessDriftMaxConsecutiveFailures), and the cap
// docs/UTILITIES-PLAN.md §3.5 specified, keyed per (utility, desired source
// hash). Recovery is anything that changes the situation: editing the
// assignment moves the source hash and resets the counter, and the manual
// Apply is allowed regardless of it.
const utilityDriftMaxConsecutiveFailures = 3

// ReconcileNode converges one node's utilities if what the ladder says differs
// from what the assignments now resolve to. Called from the node service on
// every managed-OS system-info report (watcher and netbooted nodes are
// excluded by the caller; ApplyToNode refuses them again anyway).
func (s *Service) ReconcileNode(ctx context.Context, accountID, nodeID uuid.UUID) {
	if s.dispatcher == nil {
		return
	}
	// Resolution WITHOUT secret substitution: ResolveForNode's ConfigHash is
	// the same pre-resolution hash ApplyToNode records as DesiredSourceHash,
	// so drift can be decided by comparing the two. Resolving here instead
	// would stamp last_used_at on every secret a utility references at every
	// reconnect, i.e. fake usage — the same reason the OU rollup compares
	// source hashes.
	set, _, err := s.ResolveForNode(ctx, accountID, nodeID)
	if err != nil {
		s.logger.WarnContext(ctx, "utility reconcile could not resolve the node's set",
			"node_id", nodeID, "error", err)
		return
	}
	if len(set) == 0 {
		// Nothing resolves here. Pruning stale rows is the apply path's job
		// (it holds the authoritative keep-list); a reconcile that dispatched
		// on an empty set would mean "uninstall", which unassign deliberately
		// does not do — unassign RETAINS.
		return
	}

	states, err := s.states.ListByNode(ctx, accountID, nodeID)
	if err != nil {
		s.logger.WarnContext(ctx, "utility reconcile could not read the ladder",
			"node_id", nodeID, "error", err)
		return
	}
	byID := make(map[string]utility.NodeState, len(states))
	for _, st := range states {
		byID[st.UtilityID] = st
	}

	drifted, exhausted := driftingUtilities(set, byID)
	for _, id := range exhausted {
		s.logger.InfoContext(ctx, "utility reconcile stood down after repeated failures",
			"node_id", nodeID, "utility", id, "failures", utilityDriftMaxConsecutiveFailures)
	}
	if len(drifted) == 0 {
		return
	}

	// An apply already registered, sent or in progress owns this node's
	// convergence. Without the check the loop races the dispatch worker on the
	// very flow it exists for — an assignment edited while the node was
	// offline, delivered on reconnect. Registered counts: a queued apply is
	// waiting for exactly this node and re-dispatching would supersede it for
	// no gain. Fails CLOSED — an unreadable count is not a licence to dispatch.
	if s.applyCounter != nil {
		live, err := s.applyCounter.CountLiveByNodeAndSlug(ctx, accountID, nodeID, node_command.SlugApplyUtilityConfig)
		if err != nil {
			s.logger.WarnContext(ctx, "utility reconcile could not count live applies; not dispatching",
				"node_id", nodeID, "error", err)
			return
		}
		if live > 0 {
			return
		}
	}

	// The per-account pacing bound applies to the loop as much as to a
	// fan-out: a rack powering back on reports system info for every node at
	// once, and each report would otherwise dispatch its own apply.
	if s.allowedUnderPacing(ctx, accountID, 1) < 1 {
		s.logger.InfoContext(ctx, "utility reconcile deferred by the concurrent-apply bound", "node_id", nodeID)
		return
	}

	// The dispatch carries the WHOLE resolved set, not just the drifted
	// members: apply_utility_config is wholesale by design (the union hash is
	// the supersede key, and the agent converges the set as a unit). That is
	// not wasteful on the device — the agent's appliers are idempotent by
	// construction, so a converged utility short-circuits. What it does cost
	// is one command per reconcile while any member keeps failing, which is
	// precisely what the 3-strike cap bounds.
	s.logger.InfoContext(ctx, "reconciling drifted utilities", "node_id", nodeID, "utilities", drifted)
	// uuid.Nil for the user: the platform did this, and the command's audit
	// row records SystemActor rather than attributing it to whoever last
	// touched the assignment.
	if _, err := s.applyToNode(ctx, accountID, uuid.Nil, nodeID, nil); err != nil {
		s.logger.WarnContext(ctx, "utility reconcile apply failed", "node_id", nodeID, "error", err)
	}
}

// driftingUtilities decides, per utility, whether the ladder disagrees with
// what the assignments now resolve to — and which utilities have exhausted
// their retry budget for the config they are failing.
//
// A utility drifts when it has no ladder row (never applied here: a fresh
// joiner, or a reimaged node whose ladder install_os dropped), when its
// recorded source hash no longer matches what resolves now (the assignment or
// its params changed), or when it was recorded desired but never confirmed
// applied (the dispatch expired, the node never came back for it).
//
// A FAILED utility drifts too — that is what a self-heal is — but only while
// it has budget left. The counter is per (utility, source hash): the same
// config failing three times stands down, and an edited config starts over.
func driftingUtilities(set []utility.Resolved, byID map[string]utility.NodeState) (drifted, exhausted []string) {
	for _, r := range set {
		id := r.Definition.ID
		st, known := byID[id]
		if !known {
			drifted = append(drifted, id)
			continue
		}
		sourceChanged := st.DesiredSourceHash != r.ConfigHash
		// An empty recorded source hash is a row written before the column
		// existed; it says nothing about drift, so it is not treated as a
		// change (that would re-apply the whole fleet once on upgrade).
		if st.DesiredSourceHash == "" {
			sourceChanged = false
		}
		if sourceChanged {
			// A changed config resets the streak at SetDesired; until then
			// the stored count belongs to the OLD config and must not gate.
			drifted = append(drifted, id)
			continue
		}
		if st.Status() == utility.StatusApplied {
			continue
		}
		if st.DriftFailures >= utilityDriftMaxConsecutiveFailures {
			exhausted = append(exhausted, id)
			continue
		}
		drifted = append(drifted, id)
	}
	return drifted, exhausted
}
