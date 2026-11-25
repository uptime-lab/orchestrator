package workflow_template

import (
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

// Every starter must be a valid portable document whose graph passes the same
// validation as a user-authored template — a starter that fails to import is
// a shipping bug, caught here instead of at runtime.
func TestStartersAreValid(t *testing.T) {
	starters := Starters()
	if len(starters) == 0 {
		t.Fatal("starter catalog is empty")
	}

	seen := map[string]bool{}
	for _, starter := range starters {
		t.Run(starter.ID, func(t *testing.T) {
			if starter.ID == "" {
				t.Fatal("starter id is required")
			}
			if seen[starter.ID] {
				t.Fatalf("duplicate starter id '%s'", starter.ID)
			}
			seen[starter.ID] = true

			doc := starter.Document
			if err := doc.ValidateDocument(); err != nil {
				t.Fatalf("document envelope invalid: %v", err)
			}

			// Run the full save-time path a real import performs.
			tmpl := &WorkflowTemplate{Name: doc.Name, Description: doc.Description, Graph: doc.Graph, Status: StatusDraft}
			if err := tmpl.Normalize(); err != nil {
				t.Fatalf("graph invalid: %v", err)
			}
			if err := tmpl.Validate(); err != nil {
				t.Fatalf("template invalid: %v", err)
			}

			// Starters are account-independent by definition.
			if refs := AccountScopedParams(doc.Graph); len(refs) != 0 {
				t.Errorf("starter carries account-scoped ids: %+v", refs)
			}
		})
	}
}

func TestFindStarter(t *testing.T) {
	if _, ok := FindStarter("install-os"); !ok {
		t.Error("expected to find install-os")
	}
	if _, ok := FindStarter("no-such-starter"); ok {
		t.Error("unexpected match for unknown id")
	}
}

// An install_os step that overwrites a used disk runs only in the recovery
// system (the server refuses it elsewhere), so every such step in a starter
// sits behind a wait for recovery: a run on a node that is not there fails at
// the wait, with a message naming the state, before anything reaches a disk.
// The Install OS starter waits too, without rebooting: it is for a node that
// is already netbooted.
func TestStarterInstallStepsRunFromRecovery(t *testing.T) {
	for _, starter := range Starters() {
		waitedForRecovery := false
		for _, n := range starter.Document.Graph.Nodes {
			if n.Type == workflow_block.TypeWaitForNode && n.Params["state"] == "recovery" {
				waitedForRecovery = true
			}
			if n.Type != "command."+node_command.SlugInstallOS {
				continue
			}
			if !waitedForRecovery {
				t.Errorf("starter %q step %q installs before any wait for recovery", starter.ID, n.ID)
			}
		}
	}
}

// The restore example overwrites a used disk, so it must not run as shipped:
// with no image picked it would write the node's default OS where the
// operator expects a backup, and with a device guessed for them it would
// write the wrong disk on a board that does not boot from NVMe. Activation
// refuses it until both are chosen.
func TestRestoreStarterNeedsAnImageAndADeviceBeforeItRuns(t *testing.T) {
	starter, ok := FindStarter("restore-backup")
	if !ok {
		t.Fatal("restore-backup starter missing")
	}
	activate := func(edit func(p map[string]interface{})) error {
		doc := starter.Document
		for i, n := range doc.Graph.Nodes {
			if n.Type == "command."+node_command.SlugInstallOS {
				params := map[string]interface{}{}
				for k, v := range n.Params {
					params[k] = v
				}
				edit(params)
				doc.Graph.Nodes[i].Params = params
			}
		}
		tmpl := &WorkflowTemplate{Name: doc.Name, Description: doc.Description, Graph: doc.Graph, Status: StatusActive}
		if err := tmpl.Normalize(); err != nil {
			return err
		}
		return tmpl.Validate()
	}

	if err := activate(func(map[string]interface{}) {}); err == nil || !strings.Contains(err.Error(), "target_device") {
		t.Fatalf("unedited restore activated: err = %v, want target_device required", err)
	}
	if err := activate(func(p map[string]interface{}) { p["target_device"] = "/dev/mmcblk0" }); err == nil || !strings.Contains(err.Error(), "image_id") {
		t.Fatalf("restore with no image activated: err = %v, want image_id required", err)
	}
	if err := activate(func(p map[string]interface{}) {
		p["target_device"] = "/dev/mmcblk0"
		p["image_id"] = "6f1c2a52-3b0e-4d47-9b4c-1f0f3c1d2e3a"
	}); err != nil {
		t.Fatalf("restore with an image and a device: %v", err)
	}
}

// A capture runs in the recovery system, where the node's hostname is the
// recovery image's own, the same for every backup of the node. Image titles
// are built from the node's name, which an operator chose and which stays.
func TestStarterCapturesAreNamedAfterTheNodeName(t *testing.T) {
	for _, starter := range Starters() {
		for _, n := range starter.Document.Graph.Nodes {
			if n.Type != "command."+node_command.SlugCaptureImage {
				continue
			}
			title, _ := n.Params["title"].(string)
			if strings.Contains(title, "node.hostname") || !strings.Contains(title, "{{node.name}}") {
				t.Errorf("starter %q step %q titles its image %q, want it built from {{node.name}}", starter.ID, n.ID, title)
			}
		}
	}
}
