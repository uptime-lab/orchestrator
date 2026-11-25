package workflow_block

import (
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

func TestRegistryIntegrity(t *testing.T) {
	seenTypes := make(map[string]bool)
	seenSlugs := make(map[string]bool)

	for _, def := range Registry() {
		if def.Type == "" {
			t.Errorf("block with empty type: %+v", def)
		}
		if seenTypes[def.Type] {
			t.Errorf("duplicate block type: %s", def.Type)
		}
		seenTypes[def.Type] = true

		if def.Name == "" || def.Category == "" {
			t.Errorf("block %s: name and category are required", def.Type)
		}

		switch def.Executor {
		case ExecutorAgent:
			if def.Slug == "" {
				t.Errorf("agent block %s must have a node-command slug", def.Type)
			}
			if !strings.HasPrefix(def.Type, "command.") {
				t.Errorf("agent block %s must use the command.* namespace", def.Type)
			}
			if seenSlugs[def.Slug] {
				t.Errorf("duplicate slug: %s", def.Slug)
			}
			seenSlugs[def.Slug] = true
		case ExecutorServer, ExecutorPlugin:
			if def.Slug != "" {
				t.Errorf("non-agent block %s must not have a node-command slug", def.Type)
			}
		default:
			t.Errorf("block %s: unknown executor kind %q", def.Type, def.Executor)
		}

		for _, p := range def.Params {
			if p.Name == "" || p.Label == "" {
				t.Errorf("block %s: param name and label are required (%+v)", def.Type, p)
			}
			if p.Type == ParamEnum && len(p.EnumValues) == 0 {
				t.Errorf("block %s: enum param %s has no values", def.Type, p.Name)
			}
		}
	}
}

func TestRegistryCoversAllWorkflowSlugs(t *testing.T) {
	// All node-command slugs meant for workflows must be present, including the
	// two that the old whitelist missed.
	// provision_connection is deliberately absent: its block type
	// (command.provision_connection) survives as the stored identity, but the
	// block dispatches apply_access_config_offline.
	for _, slug := range []string{
		"reboot", "shutdown", "status_check", "install_os", "install_agent",
		"update_boot_order", "update_file", "apply_access_config_offline",
		"enable_eeprom", "erase_drive", "update_agent", "set_hostname",
		"patch_file",
	} {
		if _, ok := FindBySlug(slug); !ok {
			t.Errorf("slug %s missing from registry", slug)
		}
	}
}

func TestFindLookups(t *testing.T) {
	if _, ok := FindBySlug("no_such_slug"); ok {
		t.Error("FindBySlug must not match unknown slugs")
	}
	def, ok := FindByType("command.reboot")
	if !ok || def.Slug != "reboot" {
		t.Errorf("FindByType(command.reboot) = %+v, %v", def, ok)
	}
}

func TestValidateParams(t *testing.T) {
	tests := []struct {
		name    string
		slug    string
		payload map[string]interface{}
		wantErr string
	}{
		{"reboot empty payload ok", "reboot", nil, ""},
		{"reboot with delay ok", "reboot", map[string]interface{}{"delay_seconds": float64(5)}, ""},
		{"reboot fractional delay rejected", "reboot", map[string]interface{}{"delay_seconds": 1.5}, "must be an integer"},
		{"reboot string delay rejected", "reboot", map[string]interface{}{"delay_seconds": "5"}, "must be an integer"},
		{"install_os missing target rejected", "install_os", map[string]interface{}{"image_id": "x"}, "'target_device' is required"},
		{"install_os without image ok (policy fallback)", "install_os", map[string]interface{}{"target_device": "/dev/nvme0n1"}, ""},
		{"install_os bool reboot ok", "install_os", map[string]interface{}{"target_device": "/dev/nvme0n1", "reboot": true}, ""},
		{"install_os string reboot rejected", "install_os", map[string]interface{}{"target_device": "/dev/nvme0n1", "reboot": "yes"}, "must be a boolean"},
		{"install_os extra keys allowed", "install_os", map[string]interface{}{"target_device": "/dev/nvme0n1", "custom": 1}, ""},
		{"set_hostname empty rejected", "set_hostname", map[string]interface{}{"hostname": ""}, "is required"},
		{"set_hostname missing rejected", "set_hostname", nil, "'hostname' is required"},
		{"update_file requires content", "update_file", map[string]interface{}{"path": "/etc/x"}, "'content' is required"},
		{"update_file hash optional", "update_file", map[string]interface{}{"path": "/etc/x", "content": "hi"}, ""},
		{"boot_order strings ok", "update_boot_order", map[string]interface{}{"boot_order": []interface{}{"disk", "network"}}, ""},
		{"boot_order empty rejected", "update_boot_order", map[string]interface{}{"boot_order": []interface{}{}}, "is required"},
		{"boot_order non-strings rejected", "update_boot_order", map[string]interface{}{"boot_order": []interface{}{1}}, "array of strings"},
		{"erase_drive requires device", "erase_drive", nil, "'target_device' is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def, ok := FindBySlug(tt.slug)
			if !ok {
				t.Fatalf("slug %s not in registry", tt.slug)
			}
			err := def.ValidateParams(tt.payload)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestApplyWatchdogBlockIsServerExecuted(t *testing.T) {
	// The block is server-executed on purpose: an agent block would hand raw
	// params to the dispatch payload builder, skipping the domain bounds, the
	// stored desired state and the config hash — the same bypass the generic
	// /command endpoint is closed for. If this ever flips to ExecutorAgent, the
	// arming path has been quietly reopened.
	def, ok := FindByType(TypeApplyWatchdog)
	if !ok {
		t.Fatal("the watchdog block is missing from the registry")
	}
	if def.Executor != ExecutorServer {
		t.Errorf("executor = %q, want %q — an agent block bypasses validation", def.Executor, ExecutorServer)
	}
	if def.Slug != "" {
		t.Errorf("a server block must carry no command slug, got %q", def.Slug)
	}
	if !def.Dangerous {
		t.Error("arming a watchdog makes a device reboot itself unattended; it is dangerous")
	}

	preset, ok := paramSpec(def, "preset")
	if !ok || !preset.Required {
		t.Fatal("preset must be a required param")
	}
	// "disabled" is what makes a fleet-wide disarm possible from a run; without
	// it the block can only ever arm.
	var hasDisabled bool
	for _, v := range preset.EnumValues {
		if v == "disabled" {
			hasDisabled = true
		}
	}
	if !hasDisabled {
		t.Error("the preset enum must offer 'disabled' so a run can disarm a fleet")
	}
}

func paramSpec(def BlockDefinition, name string) (ParamSpec, bool) {
	for _, p := range def.Params {
		if p.Name == name {
			return p, true
		}
	}
	return ParamSpec{}, false
}

// The "Provision access" block keeps its stored type — every saved graph
// references command.provision_connection — but dispatches the offline access
// apply: login access comes from the resolved access entries (the Credentials
// page), not from the retired node/unit/account provisioning defaults. This is
// the ONE block whose Type and Slug differ; a second one needs its own reason.
func TestProvisionAccessBlockDispatchesTheOfflineAccessApply(t *testing.T) {
	def, ok := FindByType("command." + node_command.SlugProvisionConnection)
	if !ok {
		t.Fatal("the Provision access block is gone; saved playbooks reference its type")
	}
	if def.Slug != node_command.SlugApplyAccessConfigOffline {
		t.Fatalf("Provision access dispatches %q, want the offline access apply", def.Slug)
	}
	for _, other := range Registry() {
		if other.Slug == "" || other.Type == def.Type {
			continue
		}
		if other.Type != "command."+other.Slug {
			t.Errorf("block %q also has Type != command.<Slug>; only the re-pointed Provision access block may", other.Type)
		}
	}
}
