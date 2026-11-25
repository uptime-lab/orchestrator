package node

import (
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// Per-slug parameter shapes the dispatch switch would otherwise drop
// silently. A value of the wrong type used to vanish at the type assertion and
// the command still ran — without the header, or against a version string
// nobody validated — and reported success. Each refusal here is an operator
// refusal (400) and never quotes the value: a header can carry a credential.

// paramKind is the JSON shape a known command parameter must have.
type paramKind int

const (
	paramString paramKind = iota
	paramNumber
	paramBool
	// paramStringList is what parseListParam accepts: an array of strings or
	// one string of separated names.
	paramStringList
)

// commandParamKinds names, per slug the generic command route accepts, the
// shape of every parameter its payload builder reads. The builder's type
// assertions DROP a value of the wrong shape — a delay of "abc" became an
// immediate reboot, a timeout of "60" the agent's default — so the shape is
// checked here, before anything is written, and refused naming the parameter.
//
// Parameters with their own validator are absent on purpose: download_file's
// headers (normalizeDownloadHeaders accepts text OR an object) and
// update_boot_order's boot_order (an array checked element by element).
// Platform-authored slugs (watchdog, device profile, LED, utilities, the
// offline access apply) are not reachable from the generic route and are not
// listed. A key the table does not name is not refused: install_os and
// install_agent carry their params through verbatim.
var commandParamKinds = map[string]map[string]paramKind{
	node_command.SlugReboot:   {"delay_seconds": paramNumber},
	node_command.SlugShutdown: {"delay_seconds": paramNumber},
	node_command.SlugSetHostname: {
		"hostname": paramString,
	},
	node_command.SlugInstallOS: {
		"image_id": paramString, "target_device": paramString,
		"reboot": paramBool, "wipe": paramBool,
	},
	node_command.SlugInstallAgent: {
		"target_device": paramString, "root_partition": paramNumber,
		"agent_version": paramString, "variant": paramString,
	},
	node_command.SlugUpdateAgent: {
		"agent_version": paramString, "allow_downgrade": paramBool, "reinstall": paramBool,
	},
	node_command.SlugProvisionConnection: {
		"target_device": paramString, "root_partition": paramNumber,
	},
	node_command.SlugUpdateBootOrder: {
		"pkg_exp": paramString, "pkg_sig": paramString,
	},
	node_command.SlugUpdateFile: {
		"path": paramString, "content": paramString, "hash": paramString, "version": paramNumber,
	},
	node_command.SlugPatchFile: {
		"path": paramString, "content": paramString, "marker": paramString,
	},
	node_command.SlugPatchFileOffline: {
		"path": paramString, "content": paramString, "marker": paramString,
		"target_device": paramString, "root_partition": paramNumber,
	},
	node_command.SlugReadFile: {
		"path": paramString, "pattern": paramString, "max_bytes": paramNumber,
	},
	node_command.SlugDeleteFile: {
		"path": paramString, "backup": paramBool,
	},
	node_command.SlugWaitForFile: {
		"path": paramString, "pattern": paramString,
		"interval_seconds": paramNumber, "timeout_seconds": paramNumber,
	},
	node_command.SlugRunScript: {
		"script": paramString, "timeout_seconds": paramNumber,
	},
	node_command.SlugEraseDrive: {"target_device": paramString},
	node_command.SlugCaptureImage: {
		"title": paramString, "generalize": paramBool, "target_device": paramString,
	},
	node_command.SlugInstallPackages: {
		"packages": paramStringList, "update": paramBool,
	},
	node_command.SlugManageService: {
		"name": paramString, "action": paramString, "unit_content": paramString,
	},
	node_command.SlugDownloadFile: {
		"url": paramString, "dest_path": paramString, "checksum": paramString,
		"mode": paramString, "owner": paramString, "extract": paramBool,
	},
	node_command.SlugWriteFile: {
		"path": paramString, "content": paramString, "mode": paramString,
		"owner": paramString, "backup": paramBool,
	},
	node_command.SlugIdentify: {
		"duration_sec": paramNumber, "stop": paramBool,
		"backend": paramString, "color": paramString, "gpio": paramNumber,
	},
}

// validateCommandParamKinds refuses a known parameter of the wrong shape. A
// null counts as absent, as it does in every builder. The refusal names the
// parameter and the expected shape, never the value.
func validateCommandParamKinds(slug string, params map[string]interface{}) error {
	kinds := commandParamKinds[slug]
	if len(kinds) == 0 || len(params) == 0 {
		return nil
	}
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names) // one stable answer when several are wrong
	for _, name := range names {
		kind, known := kinds[name]
		value := params[name]
		if !known || value == nil {
			continue
		}
		if want, ok := paramKindMatches(kind, value); !ok {
			return app_errors.OperatorRefusal(fmt.Errorf("%s: %s must be %s", slug, name, want))
		}
	}
	return nil
}

func paramKindMatches(kind paramKind, value interface{}) (string, bool) {
	switch kind {
	case paramNumber:
		switch value.(type) {
		case float64, float32, int, int32, int64, uint32:
			return "", true
		}
		return "a number", false
	case paramBool:
		_, ok := value.(bool)
		return "true or false", ok
	case paramStringList:
		switch v := value.(type) {
		case string, []string:
			return "", true
		case []interface{}:
			for _, item := range v {
				if _, isString := item.(string); !isString {
					return "a list of strings", false
				}
			}
			return "", true
		}
		return "a list of strings", false
	default:
		_, ok := value.(string)
		return "a string", ok
	}
}

// commandRequiredParams names, per slug the generic command route accepts, the
// parameters its payload builder refuses to run without. A string must be
// non-blank, a list must name at least one entry. Checked beside the shape
// table and for the same reason: an operator's missing value is refused as
// theirs (400) before any check about the node's state answers instead, and
// before anything — a command row, a capture's image record — is written. The
// builder keeps its own checks as the backstop for the callers that build
// params themselves.
//
// Content-bearing params (write_file and update_file content) are absent: an
// empty file is a legitimate thing to write, and the builder requires only
// that the key is present.
var commandRequiredParams = map[string][]string{
	node_command.SlugSetHostname:      {"hostname"},
	node_command.SlugUpdateFile:       {"path"},
	node_command.SlugPatchFile:        {"content"},
	node_command.SlugPatchFileOffline: {"content"},
	node_command.SlugReadFile:         {"path"},
	node_command.SlugDeleteFile:       {"path"},
	node_command.SlugWaitForFile:      {"path", "pattern"},
	node_command.SlugRunScript:        {"script"},
	node_command.SlugEraseDrive:       {"target_device"},
	node_command.SlugCaptureImage:     {"title"},
	node_command.SlugInstallPackages:  {"packages"},
	node_command.SlugManageService:    {"name"},
	node_command.SlugDownloadFile:     {"url", "dest_path"},
	node_command.SlugWriteFile:        {"path"},
}

// validateRequiredCommandParams refuses a command whose required parameter is
// missing or blank, naming the parameter. Runs after validateCommandParamKinds,
// so a present value already has the right shape.
func validateRequiredCommandParams(slug string, params map[string]interface{}) error {
	for _, name := range commandRequiredParams[slug] {
		present := false
		if commandParamKinds[slug][name] == paramStringList {
			present = len(parseListParam(params[name])) > 0
		} else if v, ok := params[name].(string); ok {
			present = strings.TrimSpace(v) != ""
		}
		if !present {
			return app_errors.OperatorRefusal(fmt.Errorf("%s: %s is required", slug, name))
		}
	}
	return nil
}

// validateCommandPayload refuses a command whose payload is wrong in itself:
// everything the payload builder checks that the request alone decides — an
// enumeration, a path policy, a digest or header shape, a version string, a
// boot order. It runs beside the shape and required-value checks and for the
// same reason: a value the operator has to fix is answered as theirs (400)
// before any refusal about the node's state (offline, unapproved, agent too
// old, wrong boot mode) answers instead of it, and before anything is written.
// A check that needs the node — its agent version, boot device, disks, or a
// lookup — stays in the builder. The builder repeats every check here with
// the same wording, so its answer for an online node is unchanged.
func validateCommandPayload(slug string, nodeID uuid.UUID, params map[string]interface{}) error {
	switch slug {
	case node_command.SlugInstallOS, node_command.SlugInstallAgent, node_command.SlugUpdateBootOrder:
		if params == nil {
			return app_errors.OperatorRefusal(fmt.Errorf("%s command requires parameters", slug))
		}
	}
	switch slug {
	case node_command.SlugInstallOS:
		if raw, ok := params["image_id"].(string); ok && strings.TrimSpace(raw) != "" {
			if _, err := uuid.Parse(strings.TrimSpace(raw)); err != nil {
				return app_errors.OperatorRefusal(errors.New("install_os: image_id is not a valid image id"))
			}
		}

	case node_command.SlugInstallAgent:
		if v, ok := params["variant"].(string); ok && v != "" &&
			v != node_command.AgentVariantFull && v != node_command.AgentVariantWatcher {
			return app_errors.OperatorRefusal(fmt.Errorf("install_agent: unknown variant %q (allowed: full, watcher)", v))
		}

	case node_command.SlugProvisionConnection:
		// The account it creates is resolved from the provisioning policy of
		// the node, its unit or the account, and never carried in the command;
		// a credential named here was dropped, and the command ran without it.
		for _, name := range provisionConnectionPolicyParams {
			if v, present := params[name]; present && v != nil {
				return app_errors.OperatorRefusal(fmt.Errorf(
					"provision_connection: %s is not a parameter; set the provisioning username, password and SSH key on the node, its organizational unit or the account", name))
			}
		}

	case node_command.SlugUpdateAgent:
		if _, err := parseUpdateAgentParams(params); err != nil {
			return err
		}

	case node_command.SlugSetHostname:
		hostname, _ := params["hostname"].(string)
		if _, err := node_command.NormalizeHostname(hostname); err != nil {
			return fmt.Errorf("set_hostname: %w", err)
		}

	case node_command.SlugUpdateBootOrder:
		raw, ok := params["boot_order"]
		if !ok {
			return app_errors.OperatorRefusal(errors.New("boot_order parameter is required"))
		}
		items, ok := raw.([]interface{})
		if !ok {
			return app_errors.OperatorRefusal(errors.New("boot_order must be an array"))
		}
		order := make([]string, 0, len(items))
		for _, item := range items {
			str, isString := item.(string)
			if !isString {
				return app_errors.OperatorRefusal(errors.New("boot_order array must contain only strings"))
			}
			order = append(order, str)
		}
		if err := node.ValidateBootOrderArray(order); err != nil {
			return app_errors.OperatorRefusal(fmt.Errorf("invalid boot order: %w", err))
		}
		pkgExp, _ := params["pkg_exp"].(string)
		pkgSig, _ := params["pkg_sig"].(string)
		if err := requireEEPROMCapability(nodeID, pkgExp, pkgSig); err != nil {
			return app_errors.OperatorRefusal(err)
		}

	case node_command.SlugRunScript:
		script, _ := params["script"].(string)
		if err := validateRunScriptBody(script); err != nil {
			return err
		}

	case node_command.SlugUpdateFile, node_command.SlugWriteFile:
		if _, ok := params["content"].(string); !ok {
			return app_errors.OperatorRefusal(fmt.Errorf("%s command requires content", slug))
		}
		if err := refuseAccessManagedWrite(params["path"]); err != nil {
			return err
		}

	case node_command.SlugPatchFile, node_command.SlugPatchFileOffline:
		if path, ok := params["path"].(string); ok && strings.TrimSpace(path) != "" {
			if _, err := node_file.ValidateTrackPath(strings.TrimSpace(path)); err != nil {
				return app_errors.OperatorRefusal(err)
			}
		}
		if err := refuseAccessManagedWrite(params["path"]); err != nil {
			return err
		}
		if marker, ok := params["marker"].(string); ok && strings.TrimSpace(marker) != "" {
			if !patchMarkerRe.MatchString(strings.TrimSpace(marker)) {
				return app_errors.OperatorRefusal(fmt.Errorf("%s marker must match %s", slug, patchMarkerRe.String()))
			}
		}

	case node_command.SlugReadFile:
		path, _ := params["path"].(string)
		if _, err := node_file.ValidateTrackPath(path); err != nil {
			return app_errors.OperatorRefusal(err)
		}

	case node_command.SlugDeleteFile:
		path, _ := params["path"].(string)
		clean, err := node_file.ValidateTrackPath(path)
		if err != nil {
			return app_errors.OperatorRefusal(err)
		}
		if err := node_file.ValidateWritePath(clean); err != nil {
			return app_errors.OperatorRefusal(err)
		}
		if node_file.IsProtected(clean) {
			return app_errors.OperatorRefusal(fmt.Errorf("%w: %s", node_file.ErrProtectedPath, clean))
		}

	case node_command.SlugCaptureImage:
		if err := node_command.ValidateTargetDevicePayload(params); err != nil {
			return app_errors.OperatorRefusal(err)
		}

	case node_command.SlugManageService:
		action, _ := params["action"].(string)
		if _, err := normalizeManageServiceAction(action); err != nil {
			return err
		}

	case node_command.SlugDownloadFile:
		if err := refuseAccessManagedWrite(params["dest_path"]); err != nil {
			return err
		}
		if v, ok := params["checksum"].(string); ok && strings.TrimSpace(v) != "" {
			if _, err := normalizeDownloadChecksum(v); err != nil {
				return err
			}
		}
		if _, err := normalizeDownloadHeaders(params["headers"]); err != nil {
			return err
		}
	}
	return nil
}

// refuseAccessManagedWrite refuses a command that would write a file the
// device's access reconciler owns (node_file.IsAccessManaged). The name check
// is all the platform can make; the agent repeats it after resolving
// symlinks.
func refuseAccessManagedWrite(raw interface{}) error {
	path, _ := raw.(string)
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return nil
	}
	if err := node_file.ValidateWritePath(filepath.Clean(path)); err != nil {
		return app_errors.OperatorRefusal(err)
	}
	return nil
}

// validateRunScriptBody refuses a run_script body no shell can run as sent.
// A NUL byte cannot be part of a shell script: the shells either stop reading
// at it or drop it, so the device would run something other than what the
// operator wrote. The agent refuses one as well, for a platform that does not.
func validateRunScriptBody(script string) error {
	if strings.ContainsRune(script, 0) {
		return app_errors.OperatorRefusal(errors.New("run_script: the script contains a NUL byte, which a shell script cannot carry"))
	}
	return nil
}

// provisionConnectionPolicyParams are the provisioning values a caller might
// expect provision_connection to take, which it resolves from policy instead.
var provisionConnectionPolicyParams = []string{"username", "password", "ssh_key", "ssh_key_type"}

// manageServiceActions is what the agent's manage_service handler executes;
// anything else it refuses only after writing and reloading the unit file the
// command carries, so an unknown action is refused here, before dispatch.
// Empty means the default, enable_now.
var manageServiceActions = []string{"enable_now", "start", "restart", "stop", "disable_now", "status"}

// normalizeManageServiceAction returns the action manage_service will carry.
func normalizeManageServiceAction(raw string) (string, error) {
	action := strings.TrimSpace(raw)
	if action == "" {
		return "enable_now", nil
	}
	if !slices.Contains(manageServiceActions, action) {
		return "", app_errors.OperatorRefusal(fmt.Errorf("manage_service: unknown action; allowed: %s",
			strings.Join(manageServiceActions, ", ")))
	}
	return action, nil
}

// sha256HexLen is the length of a SHA-256 digest in hex, the only checksum the
// agent's download_file verifies.
const sha256HexLen = 64

// normalizeDownloadChecksum returns download_file's checksum as the bare
// lower-case hex the agent compares against, accepting the common "sha256:"
// prefix. Another algorithm, or a value that is not a SHA-256 digest, is
// refused here: on the device it fails as a bare checksum mismatch, which
// reads as a corrupt download rather than as a wrong value.
func normalizeDownloadChecksum(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if algo, digest, found := strings.Cut(value, ":"); found {
		if !strings.EqualFold(strings.TrimSpace(algo), "sha256") {
			return "", app_errors.OperatorRefusal(errors.New(
				"download_file: checksum must be a SHA-256 digest; other algorithms are not supported"))
		}
		value = strings.TrimSpace(digest)
	}
	if len(value) != sha256HexLen {
		return "", app_errors.OperatorRefusal(fmt.Errorf(
			"download_file: checksum must be a SHA-256 digest: %d hexadecimal characters, optionally prefixed with sha256:", sha256HexLen))
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", app_errors.OperatorRefusal(errors.New(
			"download_file: checksum must contain only hexadecimal characters"))
	}
	return strings.ToLower(value), nil
}

// agentVersionParamRe is what the agent CDN serves: "latest" or a release
// version, optionally v-prefixed, with a simple pre-release suffix. Anything
// else becomes part of a download URL on the device, and fails there as a
// signature error that reads like a key-trust problem.
var agentVersionParamRe = regexp.MustCompile(`^(latest|v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?)$`)

// updateAgentParams is the validated update_agent request.
type updateAgentParams struct {
	Version        string
	AllowDowngrade bool
	Reinstall      bool
}

// parseUpdateAgentParams validates update_agent's params. A missing or empty
// agent_version means "latest".
func parseUpdateAgentParams(params map[string]interface{}) (updateAgentParams, error) {
	out := updateAgentParams{Version: "latest"}
	if raw, ok := params["agent_version"]; ok && raw != nil {
		v, isString := raw.(string)
		if !isString {
			return out, app_errors.OperatorRefusal(errors.New("update_agent: agent_version must be a string"))
		}
		if v = strings.TrimSpace(v); v != "" {
			if !agentVersionParamRe.MatchString(v) {
				return out, app_errors.OperatorRefusal(errors.New(
					`update_agent: agent_version must be "latest" or a release version such as 1.2.3`))
			}
			out.Version = v
		}
	}
	var err error
	// allow_downgrade is the anti-rollback override the agent names when it
	// refuses an older package; a misspelt value must not silently read false.
	if out.AllowDowngrade, err = optionalBool(params, "update_agent", "allow_downgrade"); err != nil {
		return out, err
	}
	if out.Reinstall, err = optionalBool(params, "update_agent", "reinstall"); err != nil {
		return out, err
	}
	return out, nil
}

// sameAgentVersion reports whether an explicit requested version names the
// version the node reports. "latest" never does: what it resolves to is the
// CDN's answer, not something the platform can compare here.
func sameAgentVersion(requested, running string) bool {
	return node_command.SameAgentVersion(requested, running)
}

func optionalBool(params map[string]interface{}, slug, key string) (bool, error) {
	raw, ok := params[key]
	if !ok || raw == nil {
		return false, nil
	}
	b, isBool := raw.(bool)
	if !isBool {
		return false, app_errors.OperatorRefusal(fmt.Errorf("%s: %s must be true or false", slug, key))
	}
	return b, nil
}

// normalizeDownloadHeaders accepts download_file's headers as either the
// newline-separated "Name: value" text the editor produces or a JSON object of
// string values — the natural API shape, since the device receives a map —
// and returns the text form the command carries. Anything else is refused.
func normalizeDownloadHeaders(raw interface{}) (string, error) {
	switch v := raw.(type) {
	case nil:
		return "", nil
	case string:
		for _, line := range strings.Split(v, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			name, _, found := strings.Cut(line, ":")
			if !found || !validHeaderName(strings.TrimSpace(name)) {
				return "", app_errors.OperatorRefusal(errors.New(
					`download_file: every headers line must have the form "Name: value"`))
			}
		}
		return v, nil
	case map[string]interface{}:
		names := make([]string, 0, len(v))
		for name := range v {
			names = append(names, name)
		}
		sort.Strings(names)
		lines := make([]string, 0, len(names))
		for _, name := range names {
			value, isString := v[name].(string)
			if !isString {
				return "", app_errors.OperatorRefusal(errors.New("download_file: every headers value must be a string"))
			}
			if !validHeaderName(name) {
				return "", app_errors.OperatorRefusal(errors.New("download_file: a headers name is not a valid HTTP header name"))
			}
			// The text form is one header per line, so a line break inside a
			// value would smuggle in a second header the operator never wrote.
			if strings.ContainsAny(value, "\r\n") {
				return "", app_errors.OperatorRefusal(errors.New("download_file: a headers value must not contain a line break"))
			}
			lines = append(lines, name+": "+strings.TrimSpace(value))
		}
		return strings.Join(lines, "\n"), nil
	default:
		return "", app_errors.OperatorRefusal(errors.New(
			`download_file: headers must be "Name: value" lines or an object of string values`))
	}
}

// validHeaderName is the RFC 9110 token rule.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}
