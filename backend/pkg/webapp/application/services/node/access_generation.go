package node

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// Applied-generation self-heal — the third layer of access convergence.
//
// The desired-state hash has existed end to end since the feature shipped: the
// backend sends it as ApplyAccessConfigCommand.config_hash, the agent persists
// it in access-state.json, no-ops on a repeat, and echoes it back as
// "generation" in the command result. The backend never read that value, so
// nothing compared applied against desired and the self-heal the hash was built
// for did not happen. Layers 1 and 2 (the recovery guard, and the re-apply on a
// recovery→OS transition) covered the known cases procedurally instead.
//
// This closes it: record what the device confirmed, and on every managed-OS
// connect re-apply when it no longer matches what the platform now wants.
//
// What the device confirms is a CLAIM, and the claim is checked against a hash
// the platform computes for itself before anything is recorded — see
// RecordAccessApplied. The column is the switch that decides whether this node
// is ever pushed again, so an agent allowed to choose its value can turn off its
// own future revocations.

// AccessGenerationResolver resolves a node's current desired access hash. It is
// the same resolution the command builder does — deliberately, because a hash
// computed any other way would compare unequal to the one the device holds and
// re-apply forever.
type AccessGenerationResolver interface {
	ResolveEffectiveProvisioning(ctx context.Context, accountID, nodeID uuid.UUID) (services.EffectiveProvisioning, error)
	ResolveAccessConfig(ctx context.Context, accountID, nodeID uuid.UUID, managePasswordAuth, passwordAuthEnabled bool) (services.AccessConfig, error)
}

// accessGenerationDeps pairs the two ports the desired hash needs. They are set
// together because a resolver without the policy half computes a hash that
// excludes ssh_password_auth — the exact bug that made a policy-only change
// look converged.
type accessGenerationDeps struct {
	ou     services.OrganizationalUnitService
	access services.AccessResolver
}

// SetAccessGenerationResolvers wires desired-hash resolution. Optional: without
// it the generation comparison is skipped entirely and the platform behaves as
// it did before — layers 1 and 2 still work.
func (s *Service) SetAccessGenerationResolvers(ou services.OrganizationalUnitService, access services.AccessResolver) {
	s.accessGeneration = &accessGenerationDeps{ou: ou, access: access}
}

// RecordAccessApplied stores the generation the device confirmed it fully
// applied. Called from the command settle, the only point where that is known.
//
// The reported value is NEVER stored as given. It is the agent's word about its
// own convergence, and it decides whether the self-heal will ever push this node
// again — so a node that pre-claims a hash the platform has not asked for yet
// suppresses the apply that would have carried it. Concretely: an agent that
// reports the hash of a config with a contractor's user REMOVED is told nothing
// when the operator actually removes it, because the drift check finds applied
// and desired already equal. A revocation that never lands is the worst outcome
// this subsystem has.
//
// So the reported value is only ever accepted as a MATCH against a hash the
// platform computed itself, and what gets stored is the platform's own string.
// The agent cannot pick the value; it can only agree with it.
//
// Not recording is the safe side of every remaining case: the node keeps the
// generation it had, and the next managed-OS report re-applies on the mismatch.
// That covers a genuine race too — an access edit landing between dispatch and
// settle makes the device's (correct) report disagree with what the platform now
// wants, and re-applying is exactly right there.
func (s *Service) RecordAccessApplied(ctx context.Context, accountID, nodeID uuid.UUID, reported string) error {
	if reported == "" {
		return nil
	}
	if s.accessGeneration == nil {
		// Nothing can verify the claim. Recording it unverified would be the old
		// behaviour, and this is the one configuration where that costs nothing
		// to refuse: the self-heal that reads this column is disabled by the same
		// missing resolvers (reconcileAccessGeneration returns immediately), so a
		// stored generation would be written for no reader.
		return nil
	}
	desired, err := s.resolveAccessGeneration(ctx, accountID, nodeID)
	if err != nil {
		return fmt.Errorf("resolving the desired access generation to verify the device's report: %w", err)
	}
	if desired == "" || reported != desired {
		// Warn, not error: the caller logs an error as a platform failure, and
		// the common cause of this is a legitimate mid-flight edit rather than a
		// fault. Both hashes are safe to log — hashAccessConfig deliberately
		// excludes password VALUES so the digest can travel this widely.
		s.logger.WarnContext(ctx, "the access generation reported by the device does not match the platform's desired hash; not recording convergence",
			"node_id", nodeID, "reported", reported, "desired", desired)
		return nil
	}
	// Two columns, named. A full-row Update here would carry back whatever the
	// row looked like when this settle started and undo anything the gRPC layer
	// wrote in between — including the `restarting` pin, which exists precisely
	// to survive a reboot. Same reasoning as the operator paths; the
	// machine paths race each other rather than a human, which is a smaller harm
	// but the same bug.
	now := time.Now()
	return s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, map[string]interface{}{
		"access_applied_generation": desired,
		"access_applied_at":         now,
	})
}

// ClearAccessApplied forgets the recorded generation, so the next managed-OS
// connect makes no claim about what the device has. Used when an apply did not
// fully converge — a partial reconcile leaves the device in a state no single
// hash describes, and recording one would make the next push a no-op.
func (s *Service) ClearAccessApplied(ctx context.Context, accountID, nodeID uuid.UUID) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	// The read stays: it is what makes this a no-op when there is nothing
	// recorded, and a pointless write here would bump updated_at on every
	// partial apply. Only the WRITE is narrowed to the two columns it owns.
	if n.AccessAppliedGeneration == "" && n.AccessAppliedAt == nil {
		return nil
	}
	return s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, map[string]interface{}{
		"access_applied_generation": "",
		"access_applied_at":         nil,
	})
}

// reconcileAccessGeneration re-applies access config when what the device
// confirmed no longer matches what the platform wants.
//
// It only ever acts on a node that has a RECORDED generation — one where access
// management has converged at least once. A node it never touched has no
// desired state worth pushing, and a reconnect that started rewriting
// /etc/ssh/authorized_keys.d on every node in the fleet would be a far worse
// failure than the stale config this heals.
//
// Best-effort throughout: access self-healing must never fail a system-info
// update, and a resolution error means "we do not know", which is not grounds
// for pushing anything.
func (s *Service) reconcileAccessGeneration(ctx context.Context, accountID, nodeID uuid.UUID, applied string) {
	if s.accessGeneration == nil {
		return
	}
	if applied == "" {
		s.applyAccessOnFirstConnect(ctx, accountID, nodeID)
		return
	}
	// An apply already registered or in flight owns this. Without the check the
	// self-heal races the dispatch worker on the very flow it exists for — an
	// access edit made while the node was offline, delivered on reconnect: the
	// queued command is still `registered` for the microseconds before the
	// worker marks it sent, so this registers a second one and supersedes the
	// first. The agent then runs the superseded command anyway and its status
	// frames are dropped, leaving a delivered, executed command labelled
	// "replaced before delivery". The sibling reapplyAccessAfterRecovery has
	// said "a queued or in-flight apply is left alone" all along.
	if s.accessApplyInFlight(ctx, accountID, nodeID) {
		return
	}

	desired, err := s.resolveAccessGeneration(ctx, accountID, nodeID)
	if err != nil {
		s.logger.Warn("failed to resolve the desired access generation",
			"node_id", nodeID, "error", err)
		return
	}
	if desired == "" || desired == applied {
		return
	}
	// Attempt cap: a desired config the agent refuses for a
	// reason the platform cannot pre-check — an unmanageable user, a lockout
	// verdict only the device can reach — used to re-enqueue on EVERY reconnect,
	// forever. The first-connect path has always had its once-only guard; this
	// is the drift branch's equivalent, derived from command history rather than
	// stored, so no schema change and no counter that can drift from reality.
	if s.accessDriftStoodDown(ctx, accountID, nodeID, desired, applied) {
		return
	}
	s.logger.Info("access config drifted from the applied generation; re-applying",
		"node_id", nodeID, "applied", applied, "desired", desired)
	if err := s.EnqueueApplyAccessConfig(ctx, accountID, nodeID); err != nil {
		s.logger.Warn("failed to re-apply the drifted access config", "node_id", nodeID, "error", err)
	}
}

// accessDriftMaxConsecutiveFailures is how many times the drift self-heal will
// re-dispatch ONE desired config the device keeps failing before it stands
// down. Recovery is anything that changes the situation: an entry edit (the
// desired hash changes, so the streak no longer matches), or the manual
// re-apply endpoint (its attempt is allowed regardless, and a success resets
// everything; a failure re-arms the stand-down, which the operator just saw).
const accessDriftMaxConsecutiveFailures = 3

// accessDriftHistoryScanLimit bounds the history page the stand-down decision
// reads. Wider than the in-flight scan because unrelated commands (status
// checks, scripts) interleave with the applies it is counting; the same bound
// reapplyAccessAfterRecovery uses. If the failed applies have scrolled past it,
// the decision degrades to today's behaviour — one more re-apply — never to a
// wrongly-permanent stand-down.
const accessDriftHistoryScanLimit = 100

// accessGenerationResultKey is the result field in which the agent echoes the
// config hash it was asked to apply — on failure too, where it is the DESIRED
// hash (see settleAccessApply). That echo is what lets the stand-down tie a
// failure to the specific config that keeps being refused.
const accessGenerationResultKey = "generation"

// accessDriftStoodDown reports whether the drift self-heal should stop
// re-enqueueing this desired config, and logs loudly when it does — a stood-down
// node is one an operator has to go look at, and a silent stand-down would read
// as "converged".
//
// Best-effort like its siblings: a history lookup failure answers "stand down",
// because a missed re-apply is recoverable (an edit or the manual endpoint)
// while a dispatch loop is not.
func (s *Service) accessDriftStoodDown(ctx context.Context, accountID, nodeID uuid.UUID, desired, applied string) bool {
	if s.nodeCommandService == nil {
		return false
	}
	recent, err := s.nodeCommandService.GetNodeCommandsByNodeID(ctx, accountID, nodeID, accessDriftHistoryScanLimit, 0)
	if err != nil {
		s.logger.Warn("could not read the command history for the drift re-apply cap; not re-applying",
			"node_id", nodeID, "error", err)
		return true
	}
	if !accessDriftReapplyExhausted(recent, desired) {
		return false
	}
	s.logger.Error("standing down the access drift self-heal: the device has failed this config repeatedly; "+
		"fix the cause, then edit the entry or use the manual re-apply endpoint",
		"node_id", nodeID, "applied", applied, "desired", desired,
		"consecutive_failures", accessDriftMaxConsecutiveFailures)
	return true
}

// accessDriftReapplyExhausted decides, from a node's recent command history
// (newest first), whether re-dispatching the CURRENT desired config has already
// been tried and failed accessDriftMaxConsecutiveFailures times in a row.
//
// Only failures FOR THIS HASH count: the agent echoes the desired generation in
// its failure report, so a failure carrying a different (or no) hash belongs to
// an older config and says nothing about this one — the streak breaks and the
// re-apply proceeds. Superseded and expired applies were never delivered, so
// they neither count nor break the streak. Any success, or a failure for
// another hash, resets — which is also what lets a manual re-apply that
// SUCCEEDS re-arm the self-heal for free.
func accessDriftReapplyExhausted(recent []node_command.NodeCommand, desired string) bool {
	failures := 0
	for i := range recent { // newest first
		if recent[i].Slug != node_command.SlugApplyAccessConfig {
			continue
		}
		switch recent[i].Status {
		case node_command.StatusFailed:
			if recent[i].Result[accessGenerationResultKey] != desired {
				return false
			}
			failures++
			if failures >= accessDriftMaxConsecutiveFailures {
				return true
			}
		case node_command.StatusSuperseded, node_command.StatusExpired:
			// Never reached the device — not an attempt for or against.
		default:
			// A success, a skip, or something still in flight: the streak of
			// device refusals is broken.
			return false
		}
	}
	return false
}

// applyAccessOnFirstConnect gives a node its access config the first time the
// platform sees it on a managed OS.
//
// Without this, entries never reached a node the platform had not already
// configured: propagation on an entry edit only walks the nodes that exist at
// that moment, and the drift self-heal deliberately skips a node with no
// recorded generation. So an account-wide user created on Monday simply did not
// exist on a device adopted on Tuesday, and nothing said so.
//
// Three guards, each for a specific way this could go wrong:
//
//   - An EMPTY desired config is never applied. Empty means "lock every managed
//     user", and a node can hold managed users with no recorded generation (a
//     partial apply clears it). Pushing emptiness there would lock accounts the
//     operator still wants, on a reconnect, silently.
//   - An apply already in flight owns the node.
//   - It fires once. If an attempt is already in the recent history — succeeded,
//     failed, whatever — this stays out of the way. Re-enqueueing on every
//     system-info report would turn one permanently refused config (say, one the
//     agent rejects as locking out all access) into a command every 30 seconds
//     forever. Recovery from a failed first apply is an entry edit, which
//     propagates, or the manual re-apply endpoint.
func (s *Service) applyAccessOnFirstConnect(ctx context.Context, accountID, nodeID uuid.UUID) {
	inFlight := s.accessApplyInFlight(ctx, accountID, nodeID)
	attempted := s.accessApplyAttempted(ctx, accountID, nodeID)
	if inFlight || attempted {
		return
	}
	cfg, err := s.resolveAccessConfigForNode(ctx, accountID, nodeID)
	if err != nil {
		s.logger.Warn("failed to resolve the access config for a node's first connect",
			"node_id", nodeID, "error", err)
		return
	}
	if !shouldApplyOnFirstConnect(cfg, inFlight, attempted) {
		return
	}
	s.logger.Info("applying access config to a node for the first time",
		"node_id", nodeID, "users", len(cfg.Users), "cas", len(cfg.CAs))
	if err := s.EnqueueApplyAccessConfig(ctx, accountID, nodeID); err != nil {
		s.logger.Warn("failed to apply access config on first connect", "node_id", nodeID, "error", err)
	}
}

// shouldApplyOnFirstConnect is the decision itself, kept pure so the guards can
// be tested without standing up a command service.
func shouldApplyOnFirstConnect(cfg services.AccessConfig, inFlight, attempted bool) bool {
	if inFlight || attempted {
		return false
	}
	// The config must GRANT something. Counting entries is not enough: a user
	// with no keys is a LOCKED user by definition, so a config made only of
	// those turns the platform's first ever act on a device into locking an
	// account — the silent-lockout-on-reconnect failure this guard exists to
	// prevent, reached from the other side. For example, an account whose only
	// entry is a keyless "pi" would have every node reconciled into locking pi.
	return cfg.GrantsALogin()
}

// accessApplyAttempted reports whether this node has ever been sent an access
// apply, as far as the bounded recent-command scan can see. Best-effort: a
// lookup failure answers "yes", because a missed first apply is recoverable by
// an edit or the manual endpoint, while a dispatch loop is not.
func (s *Service) accessApplyAttempted(ctx context.Context, accountID, nodeID uuid.UUID) bool {
	if s.nodeCommandService == nil {
		return true
	}
	recent, err := s.nodeCommandService.GetNodeCommandsByNodeID(ctx, accountID, nodeID, accessInFlightScanLimit, 0)
	if err != nil {
		s.logger.Warn("could not check whether access was ever applied", "node_id", nodeID, "error", err)
		return true
	}
	for i := range recent {
		if recent[i].Slug == node_command.SlugApplyAccessConfig {
			return true
		}
	}
	return false
}

// accessApplyInFlight reports whether an access apply is already registered or
// on its way to this node. Best-effort: a lookup failure answers "yes", because
// dispatching a duplicate is the worse of the two mistakes.
func (s *Service) accessApplyInFlight(ctx context.Context, accountID, nodeID uuid.UUID) bool {
	if s.nodeCommandService == nil {
		return false
	}
	recent, err := s.nodeCommandService.GetNodeCommandsByNodeID(ctx, accountID, nodeID, accessInFlightScanLimit, 0)
	if err != nil {
		s.logger.Warn("could not check for an in-flight access apply", "node_id", nodeID, "error", err)
		return true
	}
	for i := range recent { // newest first
		if recent[i].Slug != node_command.SlugApplyAccessConfig {
			continue
		}
		switch recent[i].Status {
		case node_command.StatusRegistered, node_command.StatusSent, node_command.StatusInProgress:
			return true
		default:
			// The newest apply is terminal; nothing is pending.
			return false
		}
	}
	return false
}

// accessInFlightScanLimit bounds the recent-command page the in-flight check
// reads. Only the newest apply matters, and a node with more than this many
// commands since its last apply has not just been given one.
const accessInFlightScanLimit = 20

// resolveAccessGeneration computes the node's current desired access hash the
// same way the command builder does: the password-auth policy is resolved FIRST
// and folded into the config, because it is part of the hash.
func (s *Service) resolveAccessGeneration(ctx context.Context, accountID, nodeID uuid.UUID) (string, error) {
	cfg, err := s.resolveAccessConfigForNode(ctx, accountID, nodeID)
	if err != nil {
		return "", err
	}
	return cfg.Hash, nil
}

// resolveAccessConfigForNode resolves the node's desired access config exactly
// as the command builder does — the password-auth policy first, so it is folded
// into the hash rather than attached after it.
func (s *Service) resolveAccessConfigForNode(ctx context.Context, accountID, nodeID uuid.UUID) (services.AccessConfig, error) {
	// Deliberately NOT ResolveEffectiveProvisioning. The hash needs exactly two
	// booleans out of it; the rest of that call loads the SSH key, decrypts the
	// node's entire secret set and issues a TouchLastUsed UPDATE — on every
	// managed-OS report of every converged node, for a command that carries no
	// secrets. Both values come from the same resolver the command builder uses,
	// so the hash is unchanged by construction rather than by coincidence.
	passwordAuth, managed, err := s.accessGeneration.ou.ResolvePasswordAuthPolicy(ctx, accountID, nodeID)
	if err != nil {
		return services.AccessConfig{}, err
	}
	return s.accessGeneration.access.ResolveAccessConfig(
		ctx, accountID, nodeID, managed, passwordAuth)
}
