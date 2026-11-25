// Package workflow_block defines the declarative catalog of blocks available to
// workflow templates. It is the single source of truth for the editor palette,
// parameter forms, and server-side payload validation: adding a block here makes
// it appear in the UI and be accepted by template validation without further
// frontend changes.
package workflow_block

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// ExecutorKind tells where a block runs.
type ExecutorKind string

const (
	// ExecutorAgent blocks are dispatched to the node's agent as node commands over gRPC.
	ExecutorAgent ExecutorKind = "agent"
	// ExecutorServer blocks are executed by the backend itself (control flow, integrations).
	ExecutorServer ExecutorKind = "server"
	// ExecutorPlugin blocks are provided by integration plugins.
	ExecutorPlugin ExecutorKind = "plugin"
)

// ParamType drives both validation and the editor widget used for a parameter.
type ParamType string

const (
	ParamString    ParamType = "string"
	ParamText      ParamType = "text" // multiline string
	ParamInt       ParamType = "int"
	ParamBool      ParamType = "bool"
	ParamEnum      ParamType = "enum"
	ParamOSImage   ParamType = "os_image"   // OS image ID, rendered as an image picker
	ParamSSHKey    ParamType = "ssh_key"    // SSH key ID, rendered as a key picker
	ParamBootOrder ParamType = "boot_order" // ordered boot device list widget
	ParamExpr      ParamType = "expr"       // a {{...}}-capable expression string
	// ParamCron is a 5-field cron expression. It is a string as far as
	// validation is concerned; the separate type exists so the editor can render
	// the schedule widget — the same reason os_image and boot_order are types
	// rather than annotated strings.
	ParamCron ParamType = "cron"
)

// ParamSpec describes a single block parameter.
type ParamSpec struct {
	Name        string
	Label       string
	Description string
	// Details is the optional expanded help behind the description — the
	// tooltip/details tier the UI shows on demand.
	Details     string `json:"details,omitempty"`
	Type        ParamType
	Required    bool
	Default     interface{}
	EnumValues  []string
	Placeholder string
	// Min/Max bound a numeric parameter, for the editor to render (the
	// registry drives generated forms). Advisory here on purpose: the
	// ENFORCEMENT is a dispatch-time clamp (see node_command.ClampDelaySeconds
	// and the http_request timeout ceiling), which also covers templates saved
	// before the bound existed — a save-time rejection would strand those.
	Min *float64
	Max *float64
	// Pattern is an optional regexp a string-typed LITERAL value must match at
	// save time. A value carrying a {{...}} reference is exempt — it resolves
	// at apply, where the consumer validates the resolved value (the agent's
	// check stays the backstop). A value pasted with a stray prefix (for
	// example "auth-key=" in front of a tailscale auth_key) would otherwise
	// fail only on the device; a save-time refusal with PatternHint is the
	// same check moved to where the operator still has the form open. Registry loaders must compile this at load so a bad pattern
	// fails the boot/CI validation, not the first save.
	Pattern string
	// PatternHint is the operator-facing message when Pattern does not match.
	// Empty falls back to naming the pattern, which is honest but unhelpful —
	// set it for anything a human types.
	PatternHint string
}

// BootRequirement declares the boot state a block needs on the DEVICE.
//
// A node runs either an installed managed OS or the RAM-backed netboot recovery
// bootstrap, and several blocks are only meaningful in one of them. Nothing
// derived this before, so an author had no way to know and the only feedback
// was a device-side failure whose message named a missing binary rather than
// the wrong boot mode.
//
// Read the values as CONSEQUENCES, not wishes — "needs the OS" covers three
// different behaviours and they are worth keeping apart.
type BootRequirement string

const (
	// BootAny is the zero value: the block behaves correctly in either state.
	BootAny BootRequirement = ""
	// BootManagedOS blocks are a CLAIM and nothing enforces them. In recovery
	// they either fail with a message about the wrong thing (a missing binary,
	// a failed stat) or — worse — succeed against the initramfs, writing a file
	// that evaporates on the next boot while reporting success. Stated here so
	// the editor can warn; do not read it as a guarantee.
	BootManagedOS BootRequirement = "managed_os"
	// BootDeferredToManagedOS blocks are HELD by the platform while the node is
	// netbooted and delivered once it reports a managed OS
	// (node_command.RequiresManagedOS). The run is correct either way, so this
	// is information, never a warning.
	BootDeferredToManagedOS BootRequirement = "deferred_to_managed_os"
	// BootRecovery blocks are REFUSED by the agent on a live OS. Today exactly
	// one: capture_image, whose handler checks hardware.RootIsEphemeral()
	// because a filesystem cannot be shrunk while it is the live root.
	BootRecovery BootRequirement = "recovery"
)

// Port is a named connection point on a block (graph editor, Phase 1+).
type Port struct {
	Name  string
	Label string
}

// BlockDefinition describes one block type available in workflow templates.
type BlockDefinition struct {
	Type        string // namespaced block type, e.g. "command.reboot"
	Slug        string // node-command slug for agent blocks, empty otherwise
	Executor    ExecutorKind
	Category    string
	Name        string
	Description string
	// Details is the optional expanded help behind the description — the
	// tooltip/details tier the UI shows on demand.
	Details string `json:"details,omitempty"`
	Params  []ParamSpec
	Inputs  []Port
	Outputs []Port
	// Dangerous marks a destructive or service-interrupting block. What it buys
	// today: the frontend draws a red border and prints "danger" — there is NO
	// extra run confirmation (an earlier version of this comment claimed one).
	// Until that exists, every Dangerous block must also be privileged
	// (authz.privilegedBlocks) so destructive authoring stays gated at all —
	// pinned by TestEveryDangerousBlockIsPrivileged.
	Dangerous   bool
	RebootsNode bool // execution engine waits for the node to reconnect
	// Boot is the device boot state this block needs; empty = either.
	Boot BootRequirement
	// BootNote states the case the requirement cannot express, verbatim, for a
	// block whose real precondition is a PARAMETER rather than the boot mode —
	// install_os onto a second disk from a running OS is legitimate, and the
	// agent refuses a MOUNTED target, not a live root. Empty when the
	// requirement says everything.
	BootNote string
	// Withdrawn marks a block that is no longer offered: a playbook that
	// already holds one still loads, saves and runs as before, but no playbook
	// may gain one (workflow_template.WithdrawnBlockCount). Kept in the
	// registry rather than removed because an unknown block type fails the
	// graph validation, which would make every stored playbook carrying it
	// impossible to open or edit.
	Withdrawn bool
}

// Well-known block types the engine and validation special-case.
const (
	TypeStart        = "trigger.start"    // manual entry point
	TypeSchedule     = "trigger.schedule" // cron-driven entry point
	TypeCondition    = "control.condition"
	TypeWait         = "control.wait"
	TypeJoin         = "control.join"
	TypeSecretUpsert = "server.secret_upsert"
	// TypeAttributeUpsert writes a per-node custom attribute. It goes through
	// the SAME service path the attribute HTTP endpoints use
	// (secret.Service.UpsertNodeAttribute), so key validation, the secret
	// quota (an attribute IS a secret) and the audit record are inherited
	// rather than re-implemented.
	TypeAttributeUpsert = "server.attribute_upsert"
	TypeHTTPRequest     = "integration.http_request"
	TypeSetBootPolicy   = "server.set_boot_policy"
	// TypeRebootToRecovery is the ONE-SHOT netboot, not a boot-policy change
	// plus a reboot. The difference is what happens afterwards: the platform
	// remembers the node's previous target and restores it the moment the node
	// reconnects netbooted, so a playbook that images a disk does not have to
	// remember to put the node back — and does not strand it in recovery if the
	// run fails halfway.
	TypeRebootToRecovery = "server.reboot_to_recovery"
	// TypeWaitForNode parks the run until the device is actually in the state
	// the next block needs. Without it a reboot is followed immediately by a
	// dispatch to a node that is still going down.
	TypeWaitForNode = "control.wait_for_node"
	// TypeApplyWatchdog is server-executed on purpose. apply_watchdog_config is
	// an agent command, but dispatching it from an agent block would hand raw
	// params to the payload builder and so bypass the domain bounds, the desired
	// state the node stores, and the config hash the device compares against —
	// the same bypass the generic /command endpoint is closed for. The server
	// block calls the arming service instead, exactly like the API does.
	TypeApplyWatchdog = "system.apply_watchdog"
)

// floatPtr makes a numeric bound expressible in a literal ParamSpec (a *float64
// distinguishes "bounded at zero" from "unbounded").
func floatPtr(v float64) *float64 { return &v }

// DefaultOutputPort is the port an edge leaves from when no port is specified.
const DefaultOutputPort = "out"

// Condition output ports.
const (
	PortTrue  = "true"
	PortFalse = "false"
)

var defaultPorts = struct {
	in  []Port
	out []Port
}{
	in:  []Port{{Name: "in"}},
	out: []Port{{Name: DefaultOutputPort}},
}

var conditionPorts = []Port{
	{Name: PortTrue, Label: "True"},
	{Name: PortFalse, Label: "False"},
}

// registry holds all known blocks. Order defines palette order within a category.
var registry = []BlockDefinition{
	{
		Type:     TypeStart,
		Executor: ExecutorServer, Category: "triggers", Name: "Start",
		Description: "Manual entry point of the playbook.",
		Outputs:     defaultPorts.out,
	},
	{
		Type:     TypeSchedule,
		Executor: ExecutorServer, Category: "triggers", Name: "Schedule",
		Description: "Run the playbook automatically on a cron schedule against its saved targets.",
		Params: []ParamSpec{
			// No Description on purpose. The editor renders this param as a
			// schedule builder whose own controls are labelled UTC and which
			// shows, live, what the expression means and when it next fires —
			// including the crontab(5) rules a paragraph here used to spell out
			// (Sunday is 0 or 7; two restricted day fields are an OR). Text
			// repeating what is already on screen is what made the panel
			// unreadable; do not add it back.
			{Name: "cron", Label: "Cron expression", Type: ParamCron, Required: true,
				Placeholder: "0 3 * * 1"},
		},
		Outputs: defaultPorts.out,
	},
	{
		Type: "command." + node_command.SlugReboot, Slug: node_command.SlugReboot,
		Executor: ExecutorAgent, Category: "power", Name: "Reboot",
		Description: "Reboot the node; the run continues once the node reconnects.",
		Params: []ParamSpec{
			{Name: "delay_seconds", Label: "Delay (s)", Type: ParamInt, Placeholder: "0",
				Min: floatPtr(0), Max: floatPtr(node_command.MaxDelaySeconds)},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		RebootsNode: true,
	},
	{
		Type: "command." + node_command.SlugShutdown, Slug: node_command.SlugShutdown,
		Executor: ExecutorAgent, Category: "power", Name: "Shutdown",
		Description: "Power the node off. The node will not report back until powered on again.",
		Params: []ParamSpec{
			{Name: "delay_seconds", Label: "Delay (s)", Type: ParamInt, Placeholder: "0",
				Min: floatPtr(0), Max: floatPtr(node_command.MaxDelaySeconds)},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Dangerous: true,
	},
	{
		Type: "command." + node_command.SlugStatusCheck, Slug: node_command.SlugStatusCheck,
		Executor: ExecutorAgent, Category: "diagnostics", Name: "Status check",
		Description: "Request an immediate status report from the node.",
		Inputs:      defaultPorts.in, Outputs: defaultPorts.out,
	},
	{
		Type: "command." + node_command.SlugInstallOS, Slug: node_command.SlugInstallOS,
		Executor: ExecutorAgent, Category: "os", Name: "Install OS",
		Description: "Write an OS image to the target disk, erasing everything on it.",
		Params: []ParamSpec{
			{Name: "image_id", Label: "OS image", Type: ParamOSImage,
				Description: "When left empty, the OS image from the node's effective policy is used."},
			{Name: "target_device", Label: "Target device", Type: ParamString, Required: true, Placeholder: "/dev/nvme0n1"},
			{Name: "reboot", Label: "Reboot after installation", Type: ParamBool, Default: false},
			{Name: "wipe", Label: "Overwrite a used disk", Type: ParamBool, Default: false,
				Description: "Allow installing onto a disk that already has partitions, for restore or re-imaging. Refused unless the node is running netboot recovery, and needs the OS image chosen. Leave off for a blank disk."},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Dangerous: true,
		BootNote:  "The target disk must not be in use. A blank second disk can be installed onto from the running OS; overwriting a used disk, and the disk the node runs from, require recovery.",
	},
	{
		Type: "command." + node_command.SlugInstallAgent, Slug: node_command.SlugInstallAgent,
		Executor: ExecutorAgent, Category: "os", Name: "Install agent",
		Description: "Install the orchestrator agent onto an installed OS.",
		Params: []ParamSpec{
			{Name: "target_device", Label: "Target device", Type: ParamString, Required: true, Placeholder: "/dev/nvme0n1"},
			{Name: "root_partition", Label: "Root partition", Type: ParamInt, Placeholder: "0 (auto-detect)"},
			{Name: "agent_version", Label: "Agent version", Type: ParamString, Required: true, Placeholder: "latest"},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		BootNote: "Writes to the installed system on the target disk, usually from recovery. The only requirement is that the disk is not in use.",
	},
	{
		Type: "command." + node_command.SlugUpdateAgent, Slug: node_command.SlugUpdateAgent,
		Executor: ExecutorAgent, Category: "os", Name: "Update agent",
		Description: "Update the node's agent to the selected version.",
		Params: []ParamSpec{
			{Name: "agent_version", Label: "Agent version", Type: ParamString, Default: "latest", Placeholder: "latest"},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		// Held rather than merely claimed, since update_agent became queueable
		// and managed-OS-only: the platform now defers it instead of letting it
		// reach the initramfs, so this is information for the editor and not a
		// warning. TestDeferredBlocksMatchTheDispatchersOwnList derives the
		// pairing rather than trusting either side.
		Boot: BootDeferredToManagedOS,
	},
	{
		Type: "command." + node_command.SlugSetHostname, Slug: node_command.SlugSetHostname,
		Executor: ExecutorAgent, Category: "system", Name: "Set hostname",
		Description: "Change the node's hostname.",
		Params: []ParamSpec{
			{Name: "hostname", Label: "Hostname", Type: ParamString, Required: true, Placeholder: "node-01"},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Boot: BootManagedOS,
	},
	{
		// Type and Slug differ HERE ONLY, and on purpose. The type is the
		// stored identity — every saved graph carrying a "Provision access"
		// step references it — while what the block DOES was re-pointed
		// from the legacy provision_connection command, which
		// created one user from the node/unit/account provisioning defaults,
		// to the offline access apply, which writes the node's resolved
		// access entries (the Credentials page). One source of truth for who
		// may log in; the sidebar's Provision access panel made the same move
		// earlier. Renaming the type instead would strand every saved
		// playbook on an unknown block.
		Type: "command." + node_command.SlugProvisionConnection, Slug: node_command.SlugApplyAccessConfigOffline,
		Executor: ExecutorAgent, Category: "system", Name: "Provision access",
		Description: "Write the node's assigned login access onto the installed system's disk while the node is in netboot recovery. Users, keys and passwords come from the access entries in Credentials, resolved for this node.",
		Details:     "The same access the platform applies on a running OS, delivered through a mounted disk — how credentials reach a node whose installed OS never runs an agent. A configuration that would leave no working login is refused.",
		Params: []ParamSpec{
			{Name: "target_device", Label: "Target device", Type: ParamString, Placeholder: "/dev/nvme0n1",
				Description: "The disk to mount. Left empty, the platform uses the node's only reported disk and refuses when there is more than one."},
			{Name: "root_partition", Label: "Root partition", Type: ParamInt, Placeholder: "0 (auto-detect)"},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Boot: BootRecovery,
	},
	{
		Type: "command." + node_command.SlugUpdateBootOrder, Slug: node_command.SlugUpdateBootOrder,
		Executor: ExecutorAgent, Category: "boot", Name: "Update boot order",
		Description: "Set the node's boot device order.",
		Params: []ParamSpec{
			{Name: "boot_order", Label: "Boot order", Type: ParamBootOrder, Required: true},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		// Every run of this block is refused: the device accepts a boot order
		// only inside a signed per-device bootloader package, which a block
		// cannot carry (requireEEPROMCapability). The node's bootloader
		// route (provision-eeprom) is the way to change it.
		Withdrawn: true,
	},
	{
		Type: "command." + node_command.SlugEnableEEPROM, Slug: node_command.SlugEnableEEPROM,
		Executor: ExecutorAgent, Category: "boot", Name: "Allow EEPROM flashing from the OS",
		Description: "Let the platform flash signed EEPROM updates from the running OS. On a CM4 this reboots the node immediately.",
		Details:     "Without this, EEPROM updates are staged on SD or USB media and applied with a reboot. The OS's own unattended EEPROM updates stay disabled. On a CM4 the block also updates SPI settings, which is why it reboots. Other boards do not reboot.",
		Inputs:      defaultPorts.in, Outputs: defaultPorts.out,
		RebootsNode: true,
		Boot:        BootManagedOS,
	},
	{
		Type:     TypeRebootToRecovery,
		Executor: ExecutorServer, Category: "boot", Name: "Reboot to recovery",
		Description: "Reboot the node into the netboot recovery image once. The previous boot target is restored when the node reconnects, so a later Reboot returns it to its OS. Use before Capture image or Install OS.",
		Inputs:      defaultPorts.in, Outputs: defaultPorts.out,
		Dangerous:   true,
		RebootsNode: true,
	},
	{
		Type:     TypeWaitForNode,
		Executor: ExecutorServer, Category: "control", Name: "Wait for node",
		Description: "Pause until the node is in the state the next block needs: back online, running its installed OS, or running the recovery image.",
		Details:     "The block fails when the timeout passes, so a device that never comes back is reported.",
		Params: []ParamSpec{
			{Name: "state", Label: "Wait until the node is", Type: ParamEnum, Required: true, Default: "online",
				EnumValues:  []string{"online", "recovery", "managed_os"},
				Description: "online = reachable; recovery = running the netboot recovery image; managed_os = online and running its installed OS."},
			{Name: "timeout_seconds", Label: "Timeout (s)", Type: ParamInt, Default: 900, Placeholder: "900",
				Min: floatPtr(1), Max: floatPtr(3600),
				Description: "Give up after this long. A reboot into recovery usually takes a minute or two; an OS install is longer."},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
	},
	{
		Type:     TypeSetBootPolicy,
		Executor: ExecutorServer, Category: "boot", Name: "Set boot policy",
		Description: "Choose whether the node boots its installed OS or netboot recovery on its next restart.",
		Details:     "This block works even while the node is rebooting. Place it before a final Reboot so a fresh install boots straight into the OS.",
		Params: []ParamSpec{
			{Name: "boot_to_os", Label: "Boot to OS", Type: ParamEnum, Default: "on",
				EnumValues:  []string{"unchanged", "inherit", "on", "off"},
				Description: "on = boot the installed OS, off = netboot recovery, inherit = clear the node override, unchanged = leave as is."},
			{Name: "network_first", Label: "Network-first boot", Type: ParamEnum, Default: "unchanged",
				EnumValues:  []string{"unchanged", "inherit", "on", "off"},
				Description: "Whether the device tries HTTP boot before local storage. unchanged = leave as is."},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
	},
	{
		Type: "command." + node_command.SlugUpdateFile, Slug: node_command.SlugUpdateFile,
		Executor: ExecutorAgent, Category: "files", Name: "Update file",
		Description: "Write a text file on the node.",
		Details:     "To set the file's mode and owner, or for an atomic write that creates parent directories, use the Write file block instead.",
		Params: []ParamSpec{
			{Name: "path", Label: "Path", Type: ParamString, Required: true, Placeholder: "/etc/config/file.conf"},
			{Name: "content", Label: "Content", Type: ParamText, Required: true},
			{Name: "hash", Label: "Hash", Type: ParamString,
				Description: "SHA256 of the content; computed automatically when left empty."},
			{Name: "version", Label: "Version", Type: ParamInt, Placeholder: "0"},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Boot: BootManagedOS,
	},
	{
		Type: "command." + node_command.SlugPatchFile, Slug: node_command.SlugPatchFile,
		Executor: ExecutorAgent, Category: "files", Name: "Patch config file",
		Description: "Insert or update a managed block in a text file without touching the rest of the file. Boot configuration changes take effect only after a reboot.",
		Details:     "Commonly used on /boot/firmware/config.txt for overclocking or PCIe settings. The original file is backed up once.",
		Params: []ParamSpec{
			{Name: "path", Label: "Path", Type: ParamString,
				Default: "/boot/firmware/config.txt", Placeholder: "/boot/firmware/config.txt",
				Description: "Defaults to /boot/firmware/config.txt when left empty."},
			{Name: "content", Label: "Options", Type: ParamText, Required: true,
				Placeholder: "[cm5]\ndtparam=pciex1_gen=3\n[all]",
				Description: "Lines to enforce inside the managed block. Raspberry Pi conditional sections such as [cm5] and [all] are honored."},
			{Name: "marker", Label: "Block name", Type: ParamString,
				Default: "orchestrator", Placeholder: "orchestrator",
				Description: "Distinct names keep independent blocks in the same file."},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Boot:     BootManagedOS,
		BootNote: "Edits the file on the running system. In recovery that is the recovery image, not the installed OS. Use Patch file (from recovery) to edit the installed system's disk.",
	}, {
		Type: "command." + node_command.SlugPatchFileOffline, Slug: node_command.SlugPatchFileOffline,
		Executor: ExecutorAgent, Category: "files", Name: "Patch file (from recovery)",
		Description: "Edit a managed block on the installed system's disk while the node is in netboot recovery. Paths under /boot/firmware/ go to the boot partition; everything else to the root partition.",
		Params: []ParamSpec{
			{Name: "path", Label: "Path", Type: ParamString,
				Default: "/boot/firmware/config.txt", Placeholder: "/boot/firmware/config.txt",
				Description: "The path as the installed system sees it."},
			{Name: "content", Label: "Options", Type: ParamText, Required: true,
				Description: "Lines to enforce inside the managed block."},
			{Name: "marker", Label: "Block name", Type: ParamString,
				Default: "orchestrator", Placeholder: "orchestrator"},
			{Name: "target_device", Label: "Target device", Type: ParamString,
				Description: "The disk to mount. Left empty, the platform uses the node's only reported disk and refuses when there is more than one."},
			{Name: "root_partition", Label: "Root partition", Type: ParamInt, Placeholder: "0 (auto-detect)",
				Description: "Ignored for a /boot/firmware path."},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Boot: BootRecovery,
	},

	{
		Type: "command." + node_command.SlugEraseDrive, Slug: node_command.SlugEraseDrive,
		Executor: ExecutorAgent, Category: "storage", Name: "Erase drive",
		Description: "Wipe the target disk. The disk the node is currently running from is refused.",
		Details:     "To wipe the disk the node runs from, run this block while the node is in netboot recovery.",
		Params: []ParamSpec{
			{Name: "target_device", Label: "Target device", Type: ParamString, Required: true, Placeholder: "/dev/sda"},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Dangerous: true,
		BootNote:  "The target disk must not be in use; a mounted disk is refused. Wiping the disk the node runs from requires recovery; wiping any other disk does not.",
	},
	{
		Type: "command." + node_command.SlugReadFile, Slug: node_command.SlugReadFile,
		Executor: ExecutorAgent, Category: "files", Name: "Read file",
		Description: "Read a text file from the node. The content is available to later blocks as {{blocks.<id>.outputs.content}} (and the first regex capture, if any, as .match).",
		Params: []ParamSpec{
			{Name: "path", Label: "Path", Type: ParamString, Required: true, Placeholder: "/etc/myapp/agent.conf"},
			{Name: "pattern", Label: "Capture regex", Type: ParamString,
				Description: "Optional: a regex whose first capture group is exposed as .match."},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Boot:     BootManagedOS,
		BootNote: "Reads from the running system. In recovery it returns the recovery image's copy, not the installed system's, and still reports success.",
	},
	{
		Type: "command." + node_command.SlugWaitForFile, Slug: node_command.SlugWaitForFile,
		Executor: ExecutorAgent, Category: "files", Name: "Wait for file",
		Description: "Poll a file on the node until its content matches a regex or the timeout passes. The matched capture is exposed as {{blocks.<id>.outputs.match}}.",
		Params: []ParamSpec{
			{Name: "path", Label: "Path", Type: ParamString, Required: true, Placeholder: "/etc/myapp/agent.conf"},
			{Name: "pattern", Label: "Match regex", Type: ParamString, Required: true,
				Placeholder: "token=(.+)", Description: "First capture group becomes the block output .match."},
			{Name: "interval_seconds", Label: "Poll interval (s)", Type: ParamInt, Default: 10, Placeholder: "10",
				Min: floatPtr(0), Max: floatPtr(node_command.MaxBlockSeconds)},
			{Name: "timeout_seconds", Label: "Timeout (s)", Type: ParamInt, Default: 600, Placeholder: "600",
				Min: floatPtr(0), Max: floatPtr(node_command.MaxBlockSeconds)},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Boot: BootManagedOS,
	},
	{
		Type: "command." + node_command.SlugRunScript, Slug: node_command.SlugRunScript,
		Executor: ExecutorAgent, Category: "system", Name: "Run script",
		Description: "Run a shell script on the node. A non-zero exit fails the block.",
		Details:     "The results are exposed as {{blocks.<id>.outputs.exit_code}}, {{blocks.<id>.outputs.stdout}} and {{blocks.<id>.outputs.stderr}}.",
		Params: []ParamSpec{
			{Name: "script", Label: "Script", Type: ParamText, Required: true,
				Placeholder: "#!/bin/sh\napt-get install -y my-package"},
			{Name: "timeout_seconds", Label: "Timeout (s)", Type: ParamInt, Default: 300, Placeholder: "300",
				Min: floatPtr(0), Max: floatPtr(node_command.MaxBlockSeconds)},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Dangerous: true,
	},
	{
		Type: "command." + node_command.SlugCaptureImage, Slug: node_command.SlugCaptureImage,
		Executor: ExecutorAgent, Category: "os", Name: "Capture image",
		Description: "Capture a disk image of the node and upload it to the platform. Must run from netboot recovery: place Reboot to recovery and Wait for node before this block.",
		Details:     "With Generalize on, machine identity is stripped to build a reusable golden image. With it off, the capture is a byte-exact backup of this device. The new image id is exposed as {{blocks.<id>.outputs.image_id}}. Deploy or restore it with Install OS.",
		Params: []ParamSpec{
			{Name: "title", Label: "Image title", Type: ParamString, Required: true, Placeholder: "golden-ubuntu-ci"},
			{Name: "generalize", Label: "Generalize (golden image)", Type: ParamBool, Default: false,
				Description: "Off: a byte-exact backup of this device. On: a reusable golden image with machine identity stripped.",
				Details:     "Generalizing strips the machine-id, SSH host keys and adoption file, producing a golden template deployable onto any device. The source node's identity is restored after the capture. Off is the default."},
			{Name: "target_device", Label: "Target device", Type: ParamString, Placeholder: "/dev/nvme0n1 (defaults to the only disk)"},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Dangerous: true,
		Boot:      BootRecovery,
	},

	// --- Software provisioning blocks (agent-executed) ---
	{
		Type: "command." + node_command.SlugInstallPackages, Slug: node_command.SlugInstallPackages,
		Executor: ExecutorAgent, Category: "software", Name: "Install packages",
		Description: "Install OS packages with apt-get (non-interactive). Output is exposed as {{blocks.<id>.outputs.stdout}}.",
		Params: []ParamSpec{
			{Name: "packages", Label: "Packages", Type: ParamText, Required: true,
				Placeholder: "curl\ngit\nopenjdk-17-jre-headless",
				Description: "One package per line (or comma/space separated); optionally name=version."},
			{Name: "update", Label: "Run apt-get update first", Type: ParamBool, Default: true},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Dangerous: true,
		Boot:      BootManagedOS,
	},
	{
		Type: "command." + node_command.SlugDownloadFile, Slug: node_command.SlugDownloadFile,
		Executor: ExecutorAgent, Category: "software", Name: "Download file",
		Description: "Download a file over http(s) onto the node, optionally verifying a SHA-256 checksum and unpacking tar archives. Use {{secrets.<name>}} in a header for authenticated downloads.",
		Params: []ParamSpec{
			{Name: "url", Label: "URL", Type: ParamExpr, Required: true, Placeholder: "https://example.com/app.tar.gz"},
			{Name: "dest_path", Label: "Destination", Type: ParamString, Required: true,
				Placeholder: "/opt/app", Description: "File path, or extraction directory when Extract is on."},
			{Name: "checksum", Label: "SHA-256", Type: ParamString, Description: "Optional expected SHA-256 of the download: 64 hex characters as sha256sum prints them, or the same with a sha256: prefix, which the server removes before dispatch."},
			{Name: "extract", Label: "Extract tar archive", Type: ParamBool, Default: false},
			{Name: "mode", Label: "File mode", Type: ParamString, Placeholder: "0755"},
			{Name: "owner", Label: "Owner", Type: ParamString, Placeholder: "root:root"},
			{Name: "headers", Label: "Headers", Type: ParamText,
				Placeholder: "Authorization: Bearer {{secrets.repo_token}}",
				Description: "One 'Name: value' per line; {{...}} references allowed."},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Dangerous: true,
		BootNote:  "In recovery the file is saved into the temporary recovery system and is lost on the next boot.",
	},
	{
		Type: "command." + node_command.SlugWriteFile, Slug: node_command.SlugWriteFile,
		Executor: ExecutorAgent, Category: "software", Name: "Write file",
		Description: "Write a configuration file with explicit path, mode and owner. The write is atomic and parent directories are created. {{secrets.*}} and {{node.*}} in the content are resolved.",
		Params: []ParamSpec{
			{Name: "path", Label: "Path", Type: ParamString, Required: true, Placeholder: "/etc/myapp/config.yaml"},
			{Name: "content", Label: "Content", Type: ParamText, Required: true},
			{Name: "mode", Label: "File mode", Type: ParamString, Placeholder: "0644"},
			{Name: "owner", Label: "Owner", Type: ParamString, Placeholder: "root:root"},
			{Name: "backup", Label: "Keep one-time backup", Type: ParamBool, Default: false},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		BootNote: "In recovery the file is written into the temporary recovery system and is lost on the next boot, even though the block reports success.",
	},
	{
		Type: "command." + node_command.SlugManageService, Slug: node_command.SlugManageService,
		Executor: ExecutorAgent, Category: "software", Name: "Manage service",
		Description: "Create/update and control a systemd unit. When unit content is provided it is written to /etc/systemd/system before the action runs. Status is exposed as {{blocks.<id>.outputs.active}}.",
		Params: []ParamSpec{
			{Name: "name", Label: "Unit name", Type: ParamString, Required: true, Placeholder: "my-agent.service"},
			{Name: "action", Label: "Action", Type: ParamEnum, Required: true, Default: "enable_now",
				EnumValues: []string{"enable_now", "start", "restart", "stop", "disable_now", "status"}},
			{Name: "unit_content", Label: "Unit file", Type: ParamText,
				Placeholder: "[Unit]\nDescription=My agent\n[Service]\nExecStart=/opt/app/run\n[Install]\nWantedBy=multi-user.target",
				Description: "Optional full unit file; written before the action runs."},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Dangerous: true,
		Boot:      BootManagedOS,
	},

	// --- Control-flow blocks (server-executed) ---
	{
		Type:     TypeCondition,
		Executor: ExecutorServer, Category: "control", Name: "Condition",
		Description: "Branch on a comparison. The left side is usually a {{...}} reference (node facts, block outputs, secrets); routes to the True or False port.",
		Params: []ParamSpec{
			{Name: "left", Label: "Left", Type: ParamExpr, Required: true, Placeholder: "{{node.model}}"},
			{Name: "op", Label: "Operator", Type: ParamEnum, Required: true,
				EnumValues: []string{"eq", "ne", "contains", "not_contains", "exists", "not_exists"},
				Default:    "eq"},
			{Name: "right", Label: "Right", Type: ParamExpr, Placeholder: "CM5"},
		},
		Inputs: defaultPorts.in, Outputs: conditionPorts,
	},
	{
		Type:     TypeWait,
		Executor: ExecutorServer, Category: "control", Name: "Wait",
		Description: "Pause the run for a fixed delay before continuing.",
		Params: []ParamSpec{
			{Name: "seconds", Label: "Seconds", Type: ParamInt, Required: true, Default: 30, Placeholder: "30"},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
	},
	{
		Type:     TypeJoin,
		Executor: ExecutorServer, Category: "control", Name: "Join",
		Description: "Merge parallel branches: the run continues once every incoming branch has arrived. This is the only block that can accept more than one incoming connection.",
		Inputs:      defaultPorts.in, Outputs: defaultPorts.out,
	},

	// --- Server / integration blocks ---
	{
		Type:     TypeSecretUpsert,
		Executor: ExecutorServer, Category: "integrations", Name: "Save secret",
		Description: "Save a value, typically a prior block's output, as a secret attached to the run's node. Useful for capturing externally issued tokens for reuse.",
		Params: []ParamSpec{
			{Name: "name", Label: "Secret name", Type: ParamString, Required: true, Placeholder: "agent_token"},
			{Name: "value", Label: "Value", Type: ParamExpr, Required: true, Placeholder: "{{blocks.wait.outputs.match}}"},
			{Name: "kind", Label: "Kind", Type: ParamEnum, Default: "opaque",
				EnumValues: []string{"opaque", "credential", "env"}},
			{Name: "env_key", Label: "Env key", Type: ParamString, Placeholder: "AGENT_TOKEN",
				Description: "Required when kind is env — the environment variable name."},
			{Name: "sensitive", Label: "Sensitive", Type: ParamBool, Default: true,
				Description: "Sensitive entries are write-only; non-sensitive env entries are visible variables."},
			{Name: "tags", Label: "Tags", Type: ParamString, Placeholder: "automation,node-specific",
				Description: "Optional comma-separated tags, unioned into the secret's tag list."},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
	},
	{
		Type:     TypeAttributeUpsert,
		Executor: ExecutorServer, Category: "integrations", Name: "Set node attribute",
		Description: "Write a custom attribute on the run's node.",
		Details:     "Later playbooks can read the attribute as {{node.attributes.<key>}}, and provisioning writes it on the node, named after the key, into the root-only file /etc/orchestrator/env (agent 0.8.5 and later; an older agent writes it into the login environment, /etc/environment). Attributes count toward the account's secret quota.",
		Params: []ParamSpec{
			{Name: "key", Label: "Attribute key", Type: ParamString, Required: true, Placeholder: "ci_agent_token",
				Description: "1-64 characters of A-Z, a-z, 0-9 or '_', not starting with a digit — a valid environment variable name."},
			{Name: "value", Label: "Value", Type: ParamExpr, Required: true, Placeholder: "{{blocks.wait.outputs.match}}"},
			{Name: "sensitive", Label: "Sensitive", Type: ParamBool, Default: false,
				Description: "Sensitive attributes are write-only in the UI. An existing sensitive attribute cannot be made non-sensitive — the block fails rather than downgrading it."},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
	},
	{
		Type:     TypeHTTPRequest,
		Executor: ExecutorServer, Category: "integrations", Name: "HTTP request",
		// The catalog must not advertise an output the engine never writes. It
		// promised a parsed `json` for months and nothing produced one, so
		// {{blocks.<id>.outputs.json.token}} — the exact use this block's own
		// description invited, feeding server.secret_upsert — resolved to an
		// empty string and wrote an EMPTY VALUE over a live secret, with the run
		// reporting COMPLETED. The resolver looks up flat keys, so emitting a
		// `json` string would not have made that expression work either; adding
		// real JSON traversal is a resolver change, not a description change.
		Description: "Call an external HTTP API.",
		Details:     "The response is exposed as {{blocks.<id>.outputs.status}} and {{blocks.<id>.outputs.body}}, and both survive a failed call so an on-error branch can read them. A body cut at 64 KiB sets {{blocks.<id>.outputs.truncated}} to true; a body that breaks mid-transfer fails the block. Use {{secrets.<name>}} in headers for authentication.",
		Params: []ParamSpec{
			{Name: "method", Label: "Method", Type: ParamEnum, Required: true, Default: "GET",
				EnumValues: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
			{Name: "url", Label: "URL", Type: ParamExpr, Required: true, Placeholder: "https://api.example.com/v1/resource"},
			{Name: "headers", Label: "Headers", Type: ParamText,
				Placeholder: "Authorization: Bearer {{secrets.api_token}}\nAccept: application/json",
				Description: "One 'Name: value' per line; {{...}} references allowed."},
			{Name: "body", Label: "Body", Type: ParamText, Placeholder: "{}"},
			{Name: "timeout_seconds", Label: "Timeout (s)", Type: ParamInt, Default: 30, Placeholder: "30"},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
	},
	{
		Type:     TypeApplyWatchdog,
		Executor: ExecutorServer, Category: "system", Name: "Set watchdog",
		Description: "Arm or disarm the node's watchdog with a preset. Requires agent 0.4.0 or later.",
		Details:     "The device refuses a configuration that would reboot-loop it, and disarms itself after repeated restarts. An offline node applies the change when it reconnects; the outputs report whether the change was applied or queued.",
		Params: []ParamSpec{
			{Name: "preset", Label: "Preset", Type: ParamEnum, Required: true, Default: "agent-health",
				EnumValues: []string{"agent-health", "os-hang", "full", "network", "disabled"},
				Description: "What the device supervises. The network preset reboots the node whenever it cannot " +
					"reach the ping target, even when the fault is on the target's side.",
				Details: "The disabled preset disarms the watchdog while keeping the stored configuration."},
			{Name: "ping_target", Label: "Ping target", Type: ParamExpr, Placeholder: "192.168.1.1",
				Description: "Used only by the network preset: the node reboots whenever it cannot reach this address."},
		},
		Inputs: defaultPorts.in, Outputs: defaultPorts.out,
		Dangerous: true,
		Boot:      BootDeferredToManagedOS,
	},
}

// Registry returns all block definitions available for workflow templates.
//
// The copy is DEEP. A shallow one shares the Params, Inputs and Outputs backing
// arrays with the package-level catalog, so any caller — a handler decorating a
// spec, a test mutating one — could rewrite the process-wide definition of a
// block for every account, permanently and invisibly.
func Registry() []BlockDefinition {
	out := make([]BlockDefinition, len(registry))
	for i, def := range registry {
		def.Params = append([]ParamSpec(nil), def.Params...)
		def.Inputs = append([]Port(nil), def.Inputs...)
		def.Outputs = append([]Port(nil), def.Outputs...)
		out[i] = def
	}
	return out
}

// ApplyDefaults returns a copy of params with every declared ParamSpec.Default
// filled in where the caller supplied nothing.
//
// It exists because `Default` was read only by the editor's form generator and
// re-implemented as a hardcoded literal at each consumption site — and for
// AGENT blocks applied nowhere at all, so `install_os.reboot`/`wipe`,
// `update_agent.agent_version` and `wait_for_file.interval_seconds` reached the
// device absent, and whatever the agent did with an absent field WAS the real
// default. Unlike Min/Max, nothing documents Default as advisory; that was an
// accident rather than a decision.
//
// "Supplied nothing" means a missing key, an explicit null, or an empty string
// — never a zero or a false. An operator who types 0 into a delay, or unticks a
// box whose default is true, has supplied a value, and a default that overrode
// it would silently do something else on the device.
func (d BlockDefinition) ApplyDefaults(params map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(params)+len(d.Params))
	for k, v := range params {
		out[k] = v
	}
	for _, spec := range d.Params {
		if spec.Default == nil {
			continue
		}
		value, present := out[spec.Name]
		if !present || value == nil {
			out[spec.Name] = spec.Default
			continue
		}
		if str, ok := value.(string); ok && str == "" {
			out[spec.Name] = spec.Default
		}
	}
	return out
}

// FindBySlug returns the block definition for a node-command slug.
func FindBySlug(slug string) (BlockDefinition, bool) {
	for _, def := range registry {
		if def.Slug != "" && def.Slug == slug {
			return def, true
		}
	}
	return BlockDefinition{}, false
}

// FindByType returns the block definition for a namespaced block type.
func FindByType(blockType string) (BlockDefinition, bool) {
	for _, def := range registry {
		if def.Type == blockType {
			return def, true
		}
	}
	return BlockDefinition{}, false
}

// IsAgent reports whether the block is dispatched to the node agent.
func (d BlockDefinition) IsAgent() bool { return d.Executor == ExecutorAgent }

// HasOutputPort reports whether the block declares the named output port.
func (d BlockDefinition) HasOutputPort(name string) bool {
	for _, port := range d.Outputs {
		if port.Name == name {
			return true
		}
	}
	return false
}

// ValidateParams checks a payload against the block's parameter specs: required
// parameters must be present and non-empty, present values must match their
// declared type. Unknown payload keys are allowed (some commands pass extra
// fields through to the agent).
func (d BlockDefinition) ValidateParams(payload map[string]interface{}) error {
	for _, spec := range d.Params {
		value, present := payload[spec.Name]
		if !present || value == nil {
			if spec.Required {
				return fmt.Errorf("parameter '%s' is required", spec.Name)
			}
			continue
		}
		if err := spec.validateValue(value); err != nil {
			return fmt.Errorf("parameter '%s': %w", spec.Name, err)
		}
	}
	// Overwriting a used disk names the image it writes. Left empty, the image
	// comes from the node's policy, which is the wrong thing to write over a
	// system the operator meant to restore from a backup.
	if d.Slug == node_command.SlugInstallOS {
		if wipe, _ := payload["wipe"].(bool); wipe {
			if image, _ := payload["image_id"].(string); strings.TrimSpace(image) == "" {
				return fmt.Errorf("parameter 'image_id' is required when 'Overwrite a used disk' is on: pick the image to write")
			}
		}
	}
	return nil
}

// ValidateParamTypes is the draft-mode variant of ValidateParams: values that
// are present must have the right type, but missing/empty required parameters
// are allowed — a draft is work in progress and cannot run anyway. Full
// validation happens when the template is activated.
func (d BlockDefinition) ValidateParamTypes(payload map[string]interface{}) error {
	for _, spec := range d.Params {
		value, present := payload[spec.Name]
		if !present || value == nil {
			continue
		}
		// An empty value of any shape counts as "not filled in yet".
		if str, ok := value.(string); ok && str == "" {
			continue
		}
		if items, ok := value.([]interface{}); ok && len(items) == 0 {
			continue
		}
		if err := spec.validateValue(value); err != nil {
			return fmt.Errorf("parameter '%s': %w", spec.Name, err)
		}
	}
	return nil
}

func (spec ParamSpec) validateValue(value interface{}) error {
	switch spec.Type {
	case ParamString, ParamText, ParamOSImage, ParamSSHKey, ParamExpr, ParamCron:
		str, ok := value.(string)
		if !ok {
			return fmt.Errorf("must be a string")
		}
		if spec.Required && str == "" {
			return fmt.Errorf("is required")
		}
		if spec.Pattern != "" && str != "" && !strings.Contains(str, "{{") {
			matched, err := regexp.MatchString(spec.Pattern, str)
			if err != nil {
				// Loaders compile patterns at registry validation, so this is
				// a hand-built ParamSpec with a broken pattern — refuse rather
				// than silently admit anything.
				return fmt.Errorf("has an invalid pattern %q: %v", spec.Pattern, err)
			}
			if !matched {
				if spec.PatternHint != "" {
					return fmt.Errorf("%s", spec.PatternHint)
				}
				return fmt.Errorf("must match %s", spec.Pattern)
			}
		}
	case ParamInt:
		// JSON numbers decode as float64; accept both.
		switch v := value.(type) {
		case float64:
			if v != float64(int64(v)) {
				return fmt.Errorf("must be an integer")
			}
		case int:
		default:
			return fmt.Errorf("must be an integer")
		}
	case ParamBool:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("must be a boolean")
		}
	case ParamEnum:
		str, ok := value.(string)
		if !ok {
			return fmt.Errorf("must be a string")
		}
		for _, allowed := range spec.EnumValues {
			if str == allowed {
				return nil
			}
		}
		return fmt.Errorf("must be one of %v", spec.EnumValues)
	case ParamBootOrder:
		items, ok := value.([]interface{})
		if !ok {
			// Already-typed []string is fine too (non-JSON callers).
			if _, ok := value.([]string); ok {
				return nil
			}
			return fmt.Errorf("must be an array of strings")
		}
		if spec.Required && len(items) == 0 {
			return fmt.Errorf("is required")
		}
		for _, item := range items {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("must be an array of strings")
			}
		}
	default:
		return fmt.Errorf("unknown parameter type '%s'", spec.Type)
	}
	return nil
}
