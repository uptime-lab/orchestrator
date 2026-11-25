// Package node_attention resolves, for one account, which nodes need an
// operator's attention.
//
// The whole account is resolved in ONE pass: the nodes, the unit tree and the
// utility ladder are each read once, the two external facts (the newest agent
// release, the signer's firmware base) are asked once, and every per-node
// verdict is then a pure call into domain/node_attention. That shape is copied
// from services/utility.LadderForOU, and for the same reason it gives there:
// calling a per-node resolver in a loop would re-read the same rows once per
// node, an O(nodes) query storm behind one page load.
//
// The service reports what it could NOT evaluate. A check whose input failed
// to load is named in Omitted and produces no items, because an empty
// attention list and an unread one look identical to a reader and only one of
// them means the fleet is fine.
package node_attention

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	attention "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_attention"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// AgentReleaseSource answers what the newest agent release published on this
// install is. "" with no error means the backend cannot enumerate releases.
type AgentReleaseSource interface {
	Latest(ctx context.Context) (string, error)
}

// BootloaderBaseSource answers what bootloader firmware the EEPROM signer
// would bake from, keyed by SoC. An empty map with no error means there is no
// signer on this install.
type BootloaderBaseSource interface {
	BaseVersions(ctx context.Context) (map[string]string, error)
}

// DeviceCapSource answers the install's effective device cap (0 = unlimited)
// — the device entitlement.
type DeviceCapSource interface {
	EffectiveDeviceCap(ctx context.Context) (cap int, subscriptionURL string)
}

// InstanceOperatorSource answers whether an account operates this install.
// The entitlement service implements it beside DeviceCapSource; a source
// without it yields the tenant view, which is the side that discloses
// nothing.
type InstanceOperatorSource interface {
	IsInstanceOperator(ctx context.Context, accountID uuid.UUID) bool
}

// AccountDeviceLimitSource answers the per-account device limit in force for
// an account (Limit 0 = none) — the entitlement service.
type AccountDeviceLimitSource interface {
	AccountDeviceLimit(ctx context.Context, accountID uuid.UUID) (entitlement.AccountLimit, error)
}

// remedySlugs are the commands the Status page can dispatch. Their most recent
// outcome per node is what lets a row say "the last attempt failed" instead of
// offering the same button again in silence — a profile apply that fails on an
// unwritable boot partition must not leave the page showing nothing at all.
var remedySlugs = []string{
	node_command.SlugUpdateAgent,
	node_command.SlugUpdateBootOrder,
	node_command.SlugApplyDeviceProfile,
	node_command.SlugApplyUtilityConfig,
	node_command.SlugReboot,
	// Not a remedy the page dispatches by that name — access is re-applied
	// through its own route — but its latest outcome is what
	// KindAccessApplyFailed is made of, and it rides the same one query.
	node_command.SlugApplyAccessConfig,
}

// FailedAttempt is a remedy that did not work, and the row to read about it.
type FailedAttempt struct {
	Slug      string
	CommandID uuid.UUID
	At        time.Time
}

// NodeAttention is one node and everything found on it.
type NodeAttention struct {
	Node node.Node
	// Busy names the command the platform has out to this node, if any, and
	// how far along it says it is. Published so a client can both withhold an
	// action from a node already being worked on — say, Apply profile pressed
	// twice while an agent update has the agent mid-restart — and SAY what is
	// running: a bare "busy" leaves an operator watching a package install of
	// several minutes with no sign of life.
	Busy *repository.ActiveCommand
	// Failures: for each remedy command, the most recent attempt on this node
	// IF it ended in failure. A failure followed by a success is not listed —
	// the question is "did the last try work", not "has this ever failed".
	Failures []FailedAttempt
	Items    []attention.Item
}

// Fleet is one account's answer.
type Fleet struct {
	// Nodes carries only nodes with at least one item.
	Nodes []NodeAttention
	// Omitted names the kinds that could not be evaluated on this run.
	Omitted []attention.Kind
}

// Service resolves the fleet's attention list.
type Service struct {
	nodes         repository.NodeRepository
	ous           repository.OrganizationalUnitRepository
	commands      repository.NodeCommandRepository
	utilityStates repository.NodeUtilityStateRepository
	releases      AgentReleaseSource
	firmware      BootloaderBaseSource
	deviceCap     DeviceCapSource
	accountLimit  AccountDeviceLimitSource
	logger        *slog.Logger
}

// SetDeviceCapSource wires the device entitlement into the cap check.
// Optional like the two sources above: without it the check is reported as
// omitted, never answered.
func (s *Service) SetDeviceCapSource(src DeviceCapSource) { s.deviceCap = src }

// SetAccountDeviceLimitSource wires the per-account device limit into the
// account-limit check. Unwired, no account limit is reported (none applies).
func (s *Service) SetAccountDeviceLimitSource(src AccountDeviceLimitSource) { s.accountLimit = src }

// NewService builds the resolver. The two sources are optional — an install
// with no signer, or a storage backend that cannot list releases, is a real
// configuration, and the checks behind them are then reported as omitted. The
// repositories are not optional.
func NewService(
	nodes repository.NodeRepository,
	ous repository.OrganizationalUnitRepository,
	commands repository.NodeCommandRepository,
	utilityStates repository.NodeUtilityStateRepository,
	releases AgentReleaseSource,
	firmware BootloaderBaseSource,
	logger *slog.Logger,
) *Service {
	if nodes == nil {
		panic("node_attention.Service requires a node repository")
	}
	if ous == nil {
		panic("node_attention.Service requires an organizational unit repository")
	}
	if commands == nil {
		panic("node_attention.Service requires a node command repository")
	}
	if utilityStates == nil {
		panic("node_attention.Service requires a node utility state repository")
	}
	if logger == nil {
		panic("node_attention.Service requires a logger")
	}
	return &Service{
		nodes:         nodes,
		ous:           ous,
		commands:      commands,
		utilityStates: utilityStates,
		releases:      releases,
		firmware:      firmware,
		logger:        logger.With("service", "node_attention"),
	}
}

// Resolve returns every node in the account that has at least one condition,
// plus the kinds this run could not judge.
//
// Only the node read is fatal. Every other input failing degrades one check
// into an omission, because a fleet view that 500s over an unreachable signer
// hides the fifteen conditions it could still have reported.
func (s *Service) Resolve(ctx context.Context, accountID uuid.UUID) (*Fleet, error) {
	nodes, err := s.nodes.GetAllByAccountID(ctx, accountID)
	if err != nil {
		return nil, err
	}

	facts := attention.Facts{Now: time.Now()}
	var omitted []attention.Kind

	if s.releases == nil {
		omitted = append(omitted, attention.KindAgentOutdated)
	} else if latest, lerr := s.releases.Latest(ctx); lerr != nil || latest == "" {
		if lerr != nil {
			s.logger.WarnContext(ctx, "could not read the agent release listing; skipping the agent-update check",
				"error", lerr, "account_id", accountID)
		}
		omitted = append(omitted, attention.KindAgentOutdated)
	} else {
		facts.LatestAgentVersion = latest
	}

	if s.firmware == nil {
		omitted = append(omitted, attention.KindBootloaderOutdated)
	} else if base, berr := s.firmware.BaseVersions(ctx); berr != nil || len(base) == 0 {
		if berr != nil {
			s.logger.WarnContext(ctx, "could not read the bootloader firmware base; skipping the bootloader check",
				"error", berr, "account_id", accountID)
		}
		omitted = append(omitted, attention.KindBootloaderOutdated)
	} else {
		facts.BaseFirmware = base
	}

	// KnownOUs stays nil on a read error, which disables the dangling-unit
	// check. Declaring every node's unit missing because one query failed
	// would be the loudest possible wrong answer.
	if ous, oerr := s.ous.GetAllByAccountID(ctx, accountID); oerr != nil {
		s.logger.WarnContext(ctx, "could not read the unit tree; skipping the dangling-unit check",
			"error", oerr, "account_id", accountID)
		omitted = append(omitted, attention.KindDanglingOU)
	} else {
		known := make(map[uuid.UUID]bool, len(ous))
		for i := range ous {
			known[ous[i].ID] = true
		}
		facts.KnownOUs = known
	}

	// An unreadable answer leaves every node looking idle, which is the same
	// state the check exists to correct — but a fleet view that refuses every
	// action because one query failed is worse, so it fails open and says so.
	busy, berr := s.commands.NodesWithActiveCommands(ctx, accountID)
	if berr != nil {
		s.logger.WarnContext(ctx, "could not read in-flight commands; actions will not be withheld for busy nodes",
			"error", berr, "account_id", accountID)
	}

	// Also fails open: without it a row simply does not mention a past
	// failure, which is where this page started.
	if len(busy) > 0 {
		active := make(map[uuid.UUID]attention.ActiveCommand, len(busy))
		for nodeID, cmd := range busy {
			active[nodeID] = attention.ActiveCommand{Slug: cmd.Slug, UpdatedAt: cmd.UpdatedAt}
		}
		facts.Active = active
	}

	attempts, aerr := s.commands.LatestAttempts(ctx, accountID, remedySlugs)
	if aerr != nil {
		s.logger.WarnContext(ctx, "could not read the latest command outcomes; failures will not be shown",
			"error", aerr, "account_id", accountID)
	}

	if len(attempts) > 0 {
		failedAccess := map[uuid.UUID]bool{}
		for nodeID, bySlug := range attempts {
			if a, ok := bySlug[node_command.SlugApplyAccessConfig]; ok &&
				a.Status == string(node_command.StatusFailed) {
				failedAccess[nodeID] = true
			}
		}
		facts.AccessApplyFailed = failedAccess
	}

	// The cap is instance-wide and asked once per page load: the effective cap
	// (cached by the entitlement service) and two counts — the install's, which
	// decides whether the cap is reached, and the caller's own, which is the
	// only one a tenant is shown.
	if s.deviceCap == nil {
		omitted = append(omitted, attention.KindDeviceCapReached)
	} else if admitted, cerr := s.nodes.CountAdmittedNodes(ctx); cerr != nil {
		s.logger.WarnContext(ctx, "could not count admitted devices; skipping the device-cap check",
			"error", cerr, "account_id", accountID)
		omitted = append(omitted, attention.KindDeviceCapReached)
	} else if own, oerr := s.nodes.CountAdmittedNodesForAccount(ctx, accountID); oerr != nil {
		s.logger.WarnContext(ctx, "could not count the account's admitted devices; skipping the device-cap check",
			"error", oerr, "account_id", accountID)
		omitted = append(omitted, attention.KindDeviceCapReached)
	} else {
		cap, _ := s.deviceCap.EffectiveDeviceCap(ctx)
		operator := false
		if op, ok := s.deviceCap.(InstanceOperatorSource); ok {
			operator = op.IsInstanceOperator(ctx, accountID)
		}
		facts.DeviceCap = &attention.DeviceCap{Cap: cap, Admitted: admitted,
			AccountDevices: own, InstanceOperator: operator}
	}

	// The account's own limit, where one binds it. A limit or a count that
	// cannot be read names the check as omitted, never answered.
	if s.accountLimit != nil {
		if limit, lerr := s.accountLimit.AccountDeviceLimit(ctx, accountID); lerr != nil {
			s.logger.WarnContext(ctx, "could not read the account's device limit; skipping the account-limit check",
				"error", lerr, "account_id", accountID)
			omitted = append(omitted, attention.KindAccountDeviceLimitReached)
		} else if limit.Applies() {
			if own, oerr := s.nodes.CountAdmittedNodesForAccount(ctx, accountID); oerr != nil {
				s.logger.WarnContext(ctx, "could not count the account's admitted devices; skipping the account-limit check",
					"error", oerr, "account_id", accountID)
				omitted = append(omitted, attention.KindAccountDeviceLimitReached)
			} else {
				facts.AccountDeviceLimit = &attention.AccountDeviceLimit{Limit: limit.Limit, AccountDevices: own}
			}
		}
	}

	if drift, derr := s.utilityDrift(ctx, accountID, nodes); derr != nil {
		s.logger.WarnContext(ctx, "could not read the utility ladder; skipping the utility drift check",
			"error", derr, "account_id", accountID)
		omitted = append(omitted, attention.KindUtilityDrift)
	} else {
		facts.Drift = drift
	}

	out := &Fleet{Omitted: omitted}
	for i := range nodes {
		items := attention.Evaluate(&nodes[i], facts)
		if len(items) == 0 {
			continue
		}
		out.Nodes = append(out.Nodes, NodeAttention{
			Node:     nodes[i],
			Busy:     activeOn(busy, nodes[i].ID),
			Failures: failuresOf(attempts[nodes[i].ID]),
			Items:    items,
		})
	}
	return out, nil
}

// utilityDrift reduces the account's utility ladder to the non-converged rows,
// in one query for the whole node set.
//
// The rung is read from the stored hashes and not recomputed. Recomputing it
// resolves {{secrets.*}} per node, which stamps last_used_at on every secret a
// utility references — a page load faking secret usage is worse than a rollup
// that lags a secret VALUE edit, which is the same trade the per-OU utility
// rollup already took.
func (s *Service) utilityDrift(ctx context.Context, accountID uuid.UUID, nodes []node.Node) (map[uuid.UUID][]attention.UtilityDrift, error) {
	if len(nodes) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(nodes))
	for i := range nodes {
		ids = append(ids, nodes[i].ID)
	}
	states, err := s.utilityStates.ListByNodes(ctx, accountID, ids)
	if err != nil {
		return nil, err
	}
	drift := map[uuid.UUID][]attention.UtilityDrift{}
	for i := range states {
		st := states[i]
		failed := st.LastError != ""
		// An empty desired hash is a row with nothing asked of it yet; that is
		// not drift. A mismatch, or an applied hash cleared by a failure, is.
		behind := st.DesiredHash != "" && st.DesiredHash != st.AppliedHash
		if !failed && !behind {
			continue
		}
		drift[st.NodeID] = append(drift[st.NodeID], attention.UtilityDrift{
			UtilityID: st.UtilityID,
			Failed:    failed,
		})
	}
	return drift, nil
}

// failuresOf keeps only the attempts that ended in failure, newest first so a
// row with several has a defined order.
func failuresOf(attempts map[string]repository.CommandAttempt) []FailedAttempt {
	if len(attempts) == 0 {
		return nil
	}
	var out []FailedAttempt
	for slug, a := range attempts {
		if a.Status != string(node_command.StatusFailed) {
			continue
		}
		out = append(out, FailedAttempt{Slug: slug, CommandID: a.CommandID, At: a.At})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// activeOn returns the node's unsettled command, or nil when it has none.
func activeOn(active map[uuid.UUID]repository.ActiveCommand, nodeID uuid.UUID) *repository.ActiveCommand {
	if cmd, ok := active[nodeID]; ok {
		return &cmd
	}
	return nil
}
