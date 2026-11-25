package results

import (
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	attention "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_attention"
)

// NodeAttentionItemResult is one condition found on one node.
//
// It carries a kind and the raw values behind it, never a sentence: the
// operator's words live in the frontend, which is the only place that can
// translate them. A `detail` string composed here would be an English string
// the UI could only print verbatim.
type NodeAttentionItemResult struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	// Current and Expected are the two sides of a comparison: a version pair
	// (agent, bootloader), or the install's admitted count and the device cap
	// (device_cap_reached — Current only for the instance operator; a tenant
	// gets Expected alone), or the account's own limit in Expected
	// (account_device_limit_reached). Empty for every other kind.
	Current  string `json:"current,omitempty"`
	Expected string `json:"expected,omitempty"`
	// Subject names what the condition is about: a command slug, a utility id,
	// the watchdog breaker's reason, the boot order that cannot reach HTTP boot.
	Subject string `json:"subject,omitempty"`
	// Count carries a number the condition is about, such as failed boot
	// attempts.
	Count int `json:"count,omitempty"`
	// Since is when the condition started, where the platform records it.
	Since *time.Time `json:"since,omitempty"`
	// AccountDevices and InstanceOperator ride on device_cap_reached, and
	// AccountDevices on account_device_limit_reached: the caller's own
	// admitted count, and whether the caller operates the install. A
	// field-name contract with the SPA.
	AccountDevices   *int64 `json:"account_devices,omitempty"`
	InstanceOperator *bool  `json:"instance_operator,omitempty"`
}

// NodeAttentionResult is one node and everything found on it.
type NodeAttentionResult struct {
	NodeID   uuid.UUID  `json:"node_id"`
	NodeName string     `json:"node_name"`
	Hostname string     `json:"hostname,omitempty"`
	OUID     *uuid.UUID `json:"ou_id,omitempty"`
	Status   string     `json:"status"`
	// InRecovery marks a node running the recovery bootstrap rather than its
	// installed OS. It is published so a client can withhold the actions that
	// would only be QUEUED there: every apply in node_command's managed-OS-only
	// set is held until the node boots its OS, so offering one produces a
	// command that sits registered with no delivery attempt and looks, to the
	// operator, like a button that did nothing.
	//
	// Deliberately not the same thing as the in_recovery CONDITION, which is
	// reported only for a connected node. This flag answers "can the platform
	// act now", and for that a last report of "recovery" is the honest input
	// whether or not the node is answering at this moment.
	InRecovery bool `json:"in_recovery,omitempty"`
	// Busy names the unsettled command the platform has out to this node. A
	// client withholds its actions while it is set — stacking a second dispatch
	// behind one that has the agent mid-restart leaves both sent and never
	// acknowledged — and shows what is running, because "busy" on its own is
	// indistinguishable from stuck.
	Busy *NodeAttentionBusyResult `json:"busy,omitempty"`
	// Failures: the most recent attempt at each remedy command that ended in
	// failure. The agent's own sentence is NOT here — it is encrypted and
	// redaction-gated, and putting it in a list would decrypt the fleet's
	// payloads on every page load. The command id is, so a client can fetch
	// the one command an operator opened through GET /node-commands/{id},
	// which redacts properly and records the read.
	Failures []NodeAttentionFailureResult `json:"failures,omitempty"`
	// Severity is the loudest of this node's items, so a list can sort and
	// colour a row without re-deriving it from the items.
	Severity string                    `json:"severity"`
	Items    []NodeAttentionItemResult `json:"items"`
}

// NodeAttentionBusyResult is what the platform is doing to a node right now.
type NodeAttentionBusyResult struct {
	Slug string `json:"slug"`
	// Progress is the agent's own 0-100, where it reports one. Zero is
	// "started, nothing reported" and is not distinguishable from 0%; the
	// slug is what carries the meaning.
	Progress int `json:"progress,omitempty"`
}

// NodeAttentionFailureResult is one remedy that did not work, and where to
// read why.
type NodeAttentionFailureResult struct {
	// Slug names the command, so a client can attach the failure to the
	// action that dispatches it.
	Slug      string    `json:"slug"`
	CommandID uuid.UUID `json:"command_id"`
	At        time.Time `json:"at"`
}

// NodeAttentionListResult is the fleet answer.
//
// Deliberately unpaginated: it is bounded by the per-account node cap, the
// same bound the node list already relies on, and a paged attention list would
// make "how many nodes need me" a question the client has to assemble.
type NodeAttentionListResult struct {
	Nodes []NodeAttentionResult `json:"nodes"`
	// Count is the number of nodes with at least one condition.
	Count int `json:"count"`
	// Counts is per kind, over the nodes this caller can see, so the filter can
	// show its numbers without the client recomputing them.
	Counts map[string]int `json:"counts"`
	// Kinds is the whole inventory in display order, so the filter is built
	// from the server and a kind added here needs no frontend release to appear.
	Kinds []string `json:"kinds"`
	// Omitted names the checks this run could not make — an unreachable signer,
	// a release listing that would not load. An empty attention list and an
	// unread one are otherwise the same JSON, and only one of them means the
	// fleet is fine.
	Omitted []string `json:"omitted"`
}

// NewNodeAttentionResult converts one node's findings.
func NewNodeAttentionResult(n *node.Node, busy *NodeAttentionBusyResult, failures []NodeAttentionFailureResult, items []attention.Item) NodeAttentionResult {
	out := NodeAttentionResult{
		NodeID:     n.ID,
		NodeName:   n.Name,
		Hostname:   n.Hostname,
		OUID:       n.OUID,
		Status:     string(n.Status),
		InRecovery: n.IsRecoveryBootstrap(),
		Busy:       busy,
		Failures:   failures,
		Severity:   string(attention.SeverityWarning),
		Items:      make([]NodeAttentionItemResult, 0, len(items)),
	}
	for _, it := range items {
		if it.Severity == attention.SeverityCritical {
			out.Severity = string(attention.SeverityCritical)
		}
		out.Items = append(out.Items, NodeAttentionItemResult{
			Kind:     string(it.Kind),
			Severity: string(it.Severity),
			Current:  it.Current,
			Expected: it.Expected,
			Subject:  it.Subject,
			Count:    it.Count,
			Since:    it.Since,

			AccountDevices:   it.AccountDevices,
			InstanceOperator: it.InstanceOperator,
		})
	}
	return out
}

// NewNodeAttentionListResult assembles the envelope, deriving the per-kind
// counts from the nodes it was given rather than from a second pass over the
// fleet — the numbers on the filter must describe the rows below it.
func NewNodeAttentionListResult(nodes []NodeAttentionResult, omitted []attention.Kind) *NodeAttentionListResult {
	counts := map[string]int{}
	for i := range nodes {
		for _, it := range nodes[i].Items {
			counts[it.Kind]++
		}
	}
	kinds := attention.AllKinds()
	kindStrings := make([]string, 0, len(kinds))
	for _, k := range kinds {
		kindStrings = append(kindStrings, string(k))
	}
	omittedStrings := make([]string, 0, len(omitted))
	for _, k := range omitted {
		omittedStrings = append(omittedStrings, string(k))
	}
	if nodes == nil {
		nodes = []NodeAttentionResult{}
	}
	return &NodeAttentionListResult{
		Nodes:   nodes,
		Count:   len(nodes),
		Counts:  counts,
		Kinds:   kindStrings,
		Omitted: omittedStrings,
	}
}
