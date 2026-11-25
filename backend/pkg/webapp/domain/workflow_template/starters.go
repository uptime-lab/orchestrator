package workflow_template

import (
	"fmt"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

// StarterTemplate is one entry of the built-in playbook catalog: a portable
// document users can instantiate instead of building a playbook from scratch.
// Instantiation goes through the normal import path, so privileged blocks
// stay authz-gated and the created playbook is always a draft.
type StarterTemplate struct {
	ID       string
	Category string
	Document PortableDocument
}

// Starters returns the built-in starter catalog: the four everyday lifecycle
// flows (install, golden image, backup, restore), kept deliberately generic —
// vendor-specific recipes belong in a shared template repository and arrive
// via import instead. Every document is composed from registry blocks only
// and must pass graph validation (enforced by tests); the list is embedded in
// code, not seeded into the database, so it evolves with the block registry.
func Starters() []StarterTemplate {
	return []StarterTemplate{
		{
			ID:       "install-os",
			Category: "os",
			Document: starterDocument(
				"Install OS",
				"Write an OS image to a blank disk, install the orchestrator agent and write the node's assigned login access, then reboot into the new system. Runs on a node already in netboot recovery; on any other node it stops at the first step. The image defaults to the node's effective policy and access comes from the access entries in Credentials; adjust the target device if the disk is not NVMe. To reinstall over a disk that already holds a system, turn on \"Overwrite a used disk\" and pick the image in the Install OS block.",
				// No reboot into recovery here, deliberately: this flow is for a
				// node that is ALREADY netbooted — that is what installing onto a
				// bare disk means — and a reboot-to-recovery would take a device
				// down for nothing. The wait checks it is there: on a node running
				// its own system it fails at once, naming the state it found.
				starterStep{
					id: "await-recovery", blockType: workflow_block.TypeWaitForNode,
					name: "Check the node is in recovery",
					params: map[string]interface{}{
						"state":           "recovery",
						"timeout_seconds": 60,
					},
				},
				starterStep{
					id: "install-os", blockType: "command." + node_command.SlugInstallOS,
					name: "Install OS",
					params: map[string]interface{}{
						"target_device": "/dev/nvme0n1",
						"reboot":        false,
						// Off: overwriting a used disk names its image
						// explicitly (the block refuses activation otherwise),
						// and this starter's image comes from the policy.
						"wipe": false,
					},
				},
				starterStep{
					id: "install-agent", blockType: "command." + node_command.SlugInstallAgent,
					name: "Install agent",
					params: map[string]interface{}{
						"target_device": "/dev/nvme0n1",
						"agent_version": "latest",
					},
				},
				starterStep{
					id: "provision-access", blockType: "command." + node_command.SlugProvisionConnection,
					name: "Provision access",
					params: map[string]interface{}{
						"target_device": "/dev/nvme0n1",
					},
				},
				starterStep{
					id: "reboot", blockType: "command." + node_command.SlugReboot,
					name:   "Reboot into the new OS",
					params: map[string]interface{}{},
				},
				// The closing wait reports the node as it will be found rather
				// than mid-reboot.
				starterStep{
					id: "await-os", blockType: workflow_block.TypeWaitForNode,
					name: "Wait for the new OS",
					params: map[string]interface{}{
						"state":           "managed_os",
						"timeout_seconds": 1800,
					},
				},
			),
		},
		{
			ID:       "capture-golden-image",
			Category: "os",
			Document: starterDocument(
				"Capture a golden image",
				"Reboot the node into netboot recovery, capture a generalized (machine-identity-stripped) disk image, upload it to the platform and return the node to its OS. The image is ready to deploy onto any device with Install OS.",
				chain(intoRecovery(), []starterStep{{
					id: "capture", blockType: "command." + node_command.SlugCaptureImage,
					name: "Capture golden image",
					params: map[string]interface{}{
						"title":      "golden-{{node.name}}",
						"generalize": true,
					},
				}}, backToOS())...,
			),
		},
		{
			ID:       "backup-node",
			Category: "os",
			Document: starterDocument(
				"Back up a node",
				"Reboot the node into netboot recovery, capture a byte-exact backup image of its disk, upload it to the platform and return the node to its OS. Restore it later with the restore playbook.",
				chain(intoRecovery(), []starterStep{{
					id: "backup", blockType: "command." + node_command.SlugCaptureImage,
					name: "Capture backup",
					params: map[string]interface{}{
						"title":      "backup-{{node.name}}",
						"generalize": false,
					},
				}}, backToOS())...,
			),
		},
		{
			ID:       "restore-backup",
			Category: "os",
			Document: starterDocument(
				"Restore from a backup",
				"Reboot the node into netboot recovery, re-image its disk from a previously captured backup or golden image, and boot into the restored system. Pick the image and the target disk in the Restore image block: the playbook cannot be activated until both are set. \"Overwrite a used disk\" is on because the target disk already contains a system. Restoring a backup onto a node other than its source strips the machine identity automatically, so the device adopts as itself.",
				chain(intoRecovery(), []starterStep{{
					id: "restore", blockType: "command." + node_command.SlugInstallOS,
					name: "Restore image",
					// No image and no disk on purpose: both are the operator's
					// choice, and a default for either writes the wrong thing
					// (the node's policy OS instead of the backup) or the wrong
					// disk (a fixed name on a board that boots from eMMC, SD or
					// USB). Activation refuses the playbook until both are set.
					params: map[string]interface{}{
						"wipe": true,
						// install_os reboots the node itself, so the epilogue is
						// only the wait — a second Reboot here would land on a
						// device that is already going down.
						"reboot": true,
					},
				}}, []starterStep{{
					id: "await-os", blockType: workflow_block.TypeWaitForNode,
					name: "Wait for the restored system",
					params: map[string]interface{}{
						"state":           "managed_os",
						"timeout_seconds": 1800,
					},
				}})...,
			),
		},
	}
}

// FindStarter returns the starter with the given id.
func FindStarter(id string) (StarterTemplate, bool) {
	for _, starter := range Starters() {
		if starter.ID == id {
			return starter, true
		}
	}
	return StarterTemplate{}, false
}

// intoRecovery is the prologue every disk-level flow needs: send the node into
// the netboot recovery image and WAIT until it is actually there.
//
// Before these two blocks existed, each of these starters carried the sentence
// "The node must be in netboot recovery: send Reboot to recovery from the node
// panel first, then run this playbook" — a manual step outside the playbook,
// which is precisely the thing a playbook is for. The one-shot also means no
// epilogue has to remember the node's previous boot target: the platform
// restored it the moment the node came back netbooted.
func intoRecovery() []starterStep {
	return []starterStep{
		{
			id: "to-recovery", blockType: workflow_block.TypeRebootToRecovery,
			name: "Reboot into recovery",
		},
		{
			id: "await-recovery", blockType: workflow_block.TypeWaitForNode,
			name: "Wait for recovery",
			params: map[string]interface{}{
				"state":           "recovery",
				"timeout_seconds": 900,
			},
		},
	}
}

// backToOS returns the node to its installed system and waits for it, so the
// run reports the node as it will be found rather than mid-reboot.
func backToOS() []starterStep {
	return []starterStep{
		{
			id: "back-to-os", blockType: "command." + node_command.SlugReboot,
			name:   "Reboot into the OS",
			params: map[string]interface{}{"delay_seconds": 0},
		},
		{
			id: "await-os", blockType: workflow_block.TypeWaitForNode,
			name: "Wait for the OS",
			params: map[string]interface{}{
				"state":           "managed_os",
				"timeout_seconds": 1800,
			},
		},
	}
}

// chain concatenates step groups into one starter body.
func chain(groups ...[]starterStep) []starterStep {
	var out []starterStep
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

// starterStep is one block of a starter chain (trigger excluded).
type starterStep struct {
	id        string
	blockType string
	name      string
	params    map[string]interface{}
}

// starterDocument assembles a portable document whose graph is a plain chain:
// trigger.start followed by the given steps, laid out vertically like
// GraphFromSteps so the canvas renders it without re-layout.
func starterDocument(name, description string, steps ...starterStep) PortableDocument {
	const (
		columnX  = 120.0
		startY   = 60.0
		rowSpace = 140.0
	)

	graph := &WorkflowGraph{
		SchemaVersion: GraphSchemaVersion,
		Nodes: []GraphNode{{
			ID:       "start",
			Type:     workflow_block.TypeStart,
			Position: GraphPosition{X: columnX, Y: startY},
		}},
		Edges: []GraphEdge{},
	}
	previousID := "start"
	for i, step := range steps {
		graph.Nodes = append(graph.Nodes, GraphNode{
			ID:       step.id,
			Type:     step.blockType,
			Name:     step.name,
			Params:   step.params,
			Position: GraphPosition{X: columnX, Y: startY + rowSpace*float64(i+1)},
		})
		graph.Edges = append(graph.Edges, GraphEdge{
			ID:       fmt.Sprintf("edge-%d", i),
			From:     previousID,
			To:       step.id,
			FromPort: workflow_block.DefaultOutputPort,
		})
		previousID = step.id
	}

	return PortableDocument{
		Kind:          PortableKind,
		SchemaVersion: PortableSchemaVersion,
		Name:          name,
		Description:   description,
		Graph:         graph,
	}
}
