package node_file

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Path policy for tracked files. Three separate lists, because they answer
// three different questions and get confused otherwise:
//
//   - PlatformPaths — files the platform itself puts on a node. They are tracked
//     without anyone asking, and reading them is not a privileged act (the two
//     config.txt paths have been readable at node-read level since long before
//     tracking existed; keeping that avoids a regression).
//   - Protected — files the platform manages or the node cannot survive losing.
//     They may be edited, never untracked, never deleted on the device.
//   - Refused — files that may not be tracked at all. This is NOT a
//     confidentiality boundary: the same permission grants run_script as root,
//     so anyone able to track a file can already read it. It exists because
//     tracking makes a durable, versioned copy on the platform, and because some
//     of these paths are not regular files at all.
var (
	// PlatformConfigTxt is the boot config the firmware actually reads, and its
	// legacy location. The agent reports both on connect.
	PlatformConfigTxt       = "/boot/firmware/config.txt"
	PlatformConfigTxtLegacy = "/boot/config.txt"

	// WatchdogConfPath is where the agent renders the watchdog daemon config.
	// The platform records the rendered text it gets back from a successful
	// apply as a tracked-file version under this path.
	WatchdogConfPath = "/etc/watchdog.conf"

	platformPaths = []string{
		PlatformConfigTxt,
		PlatformConfigTxtLegacy,
	}

	// protectedPaths cannot be untracked or deleted on the device. Prefix
	// entries end in "/" and cover everything beneath them.
	protectedPaths = []string{
		PlatformConfigTxt,
		PlatformConfigTxtLegacy,
		"/boot/firmware/cmdline.txt",
		"/etc/systemd/system.conf.d/10-orchestrator-watchdog.conf",
		WatchdogConfPath,
		"/etc/systemd/system/orchestrator-agent.service",
		"/lib/systemd/system/orchestrator-agent.service",
		"/usr/bin/orchestrator-agent",
		"/usr/local/bin/orchestrator-agent",
		"/etc/orchestrator/node-adoption.txt",
		"/mnt/sdcard/node-adoption.txt",
		"/etc/machine-id",
		"/etc/fstab",
		"/etc/passwd",
		"/etc/group",
	}
	// accessManagedPaths and accessManagedPrefixes are what the device's access
	// reconciler writes from the access entries: the key, principal and CA
	// files, the sshd login policy and the sudo grants. A tracked-file write or
	// delete of one would grant or revoke access behind the entries' back — a
	// NOPASSWD line where the entry asks for a password, a grant removed that
	// the platform believes is in place — so both are refused; reading and
	// tracking them is not. The agent refuses the same names (file_ops_policy.go
	// derives them from the reconciler's own constants); keep the two in step.
	accessManagedPaths = []string{
		"/etc/ssh/sshd_config.d/00-orchestrator-access.conf",
		"/etc/ssh/sshd_config.d/00-orchestrator.conf",
		"/etc/ssh/orchestrator_trusted_cas.pub",
	}
	accessManagedPrefixes = []string{
		"/etc/ssh/authorized_keys.d/",
		"/etc/ssh/orchestrator_ca.d/",
		"/etc/ssh/auth_principals/",
		"/etc/sudoers.d/90-orchestrator-",
	}

	// refusedPrefixes are pseudo-filesystems and volatile state: reading them is
	// either meaningless or hangs (a FIFO under /run blocks the agent's command
	// loop), and writing them back is never what the operator meant.
	// /var/run and /var/lock are the historical names for /run and /run/lock and
	// on every distro we ship to they are symlinks into it. Refusing only the
	// modern spelling meant the same FIFO was refused as /run/x and accepted as
	// /var/run/x.
	refusedPrefixes = []string{"/proc/", "/sys/", "/dev/", "/run/", "/var/run/", "/var/lock/"}

	// refusedExact are credentials whose durable copy on the platform buys
	// nothing but risk. The adoption file is the sharpest: it carries the node's
	// unique token, i.e. the ability to impersonate the node.
	refusedExact = []string{
		"/etc/shadow",
		"/etc/shadow-",
		"/etc/gshadow",
		"/etc/gshadow-",
		"/etc/orchestrator/node-adoption.txt",
		"/mnt/sdcard/node-adoption.txt",
		"/usr/bin/orchestrator-agent",
		"/usr/local/bin/orchestrator-agent",
	}

	// refusedSuffixes catch private key material wherever it lives. The .pub
	// counterparts are deliberately absent — a public key is fine to track.
	refusedSuffixes = []string{".pem", ".key", ".p12", ".pfx"}
)

// MaxContentBytes bounds one stored file version.
//
// It is not a storage limit, which is why — alone among the numbers here — it
// is not configurable. The content travels to the device inside a single gRPC
// command frame, and an oversized frame does not fail the command: it tears down
// the node's stream and takes the node offline until it reconnects. An operator
// raising a quota must not be able to knock their fleet offline as a side effect.
//
// This comment used to say "both sides run gRPC's 4 MiB default". That stopped
// being true when the ceiling was made explicit and symmetric — maxGRPCMessageBytes,
// 8 MiB, in infrastructure/grpc/server.go and the agent's platform_client.go —
// precisely because the library defaults were asymmetric (4 MiB receive,
// effectively unlimited send). Do not size a future limit against 4 MiB, and do
// not size it against 8 MiB either: the point is that this cap stays far below
// whatever the frame ceiling happens to be, so the two can move independently.
// 512 KiB is a configuration file by any measure, and it leaves the frame budget
// untouched.
const MaxContentBytes = 512 * 1024

// Limits are the tracked-file quotas an install can tune. They are quotas, not
// safety bounds (MaxContentBytes above is the safety bound and stays fixed), so
// they follow the repo's quota convention: 0 means unlimited, and an install
// runs unrestricted unless it configures them.
type Limits struct {
	// VersionsPerPath is how much history one tracked file keeps.
	VersionsPerPath int
	// FilesPerNode is how many files one node may track.
	FilesPerNode int
	// BytesPerNode is the stored-content budget for one node, counted as bytes
	// actually in the table (ciphertext, about a third larger than the plaintext
	// it holds).
	BytesPerNode int64
	// BytesPerAccount is the same budget across every node of an account. The
	// per-node budget alone still multiplies: a thousand nodes at 16 MB is 16 GB
	// from one tenant, which is why this exists. 0 by default — an install that
	// has not thought about the number should not have one imposed.
	BytesPerAccount int64
}

// DefaultLimits are the values the feature shipped with, kept as the defaults so
// an install that configures nothing behaves exactly as before.
func DefaultLimits() Limits {
	return Limits{
		VersionsPerPath: MaxVersionsPerPath,
		FilesPerNode:    MaxTrackedPerNode,
		BytesPerNode:    MaxTotalBytesPerNode,
		BytesPerAccount: 0,
	}
}

// MaxVersionsPerPath is how much history one tracked file keeps. Without a
// bound, an editor that saves on every keystroke-sized change grows the table
// forever, and each row holds the whole file.
const MaxVersionsPerPath = 20

// LooksLikeStoredCiphertext reports whether content is the at-rest envelope
// rather than a file's real text.
//
// It guards the case of a backend without the decryption code serving a
// database whose rows are already encrypted: the editor would show
// "v1:<base64>…" and Apply would write that into a device's config.txt. The at-rest format is a prefix plus one
// unbroken run of base64, which no configuration file is.
func LooksLikeStoredCiphertext(content string) bool {
	for _, prefix := range []string{"v1:", "encv1:"} {
		body, found := strings.CutPrefix(content, prefix)
		if !found || len(body) < 32 || strings.ContainsAny(body, "\n\r \t") {
			continue
		}
		for _, r := range body {
			isBase64 := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
				(r >= '0' && r <= '9') || r == '+' || r == '/' || r == '='
			if !isBase64 {
				return false
			}
		}
		return true
	}
	return false
}

// MaxTrackedPerNode is how many files one node may track.
const MaxTrackedPerNode = 50

// MaxTotalBytesPerNode is the stored-content budget for one node, counted as
// the bytes actually in the table (ciphertext, which runs about a third larger
// than the plaintext it holds).
//
// The per-file and per-version limits alone still multiply: 50 files x 20
// versions x 512 KB is half a gigabyte from a single node, and a fleet is
// thousands of nodes. This is the ceiling that makes the feature's worst case a
// number rather than a multiplication.
const MaxTotalBytesPerNode = 16 * 1024 * 1024

// MaxPathLen bounds a tracked path. node_files.path is a varchar(500), so this
// is the column, not an arbitrary choice.
const MaxPathLen = 500

// IsPlatformPath reports whether the path is one the platform puts on the node
// itself (as opposed to one a user asked to track).
func IsPlatformPath(path string) bool {
	for _, p := range platformPaths {
		if path == p {
			return true
		}
	}
	return false
}

// IsProtected reports whether the path may not be untracked or deleted.
func IsProtected(path string) bool {
	for _, p := range protectedPaths {
		if path == p {
			return true
		}
	}
	return IsAccessManaged(path)
}

// IsAccessManaged reports whether the path is one the device's access
// reconciler writes, which no tracked-file write, patch or delete may touch.
func IsAccessManaged(path string) bool {
	for _, p := range accessManagedPaths {
		if path == p {
			return true
		}
	}
	for _, prefix := range accessManagedPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// ValidateWritePath refuses a write to a path the access reconciler owns. It
// takes a path that has already passed ValidateTrackPath.
func ValidateWritePath(path string) error {
	if IsAccessManaged(path) {
		return fmt.Errorf("%w: %s", ErrAccessManagedPath, path)
	}
	return nil
}

// ValidateTrackPath canonicalises and vets a path a user asked to track. It
// returns the path unchanged on success so callers can use the validated value.
//
// This runs on the platform for a clear error message; the agent enforces the
// same rules independently, because a check that lives only here is one backend
// bug away from being no check at all.
func ValidateTrackPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: path is required", ErrInvalidPath)
	}
	if strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("%w: path contains a null byte", ErrInvalidPath)
	}
	if len(path) > MaxPathLen {
		return "", fmt.Errorf("%w: path is longer than %d characters", ErrInvalidPath, MaxPathLen)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path must be absolute", ErrInvalidPath)
	}
	// Clean collapses "..", so comparing against it rejects traversal and any
	// non-canonical spelling (trailing slash, doubled separators) in one test.
	if filepath.Clean(path) != path {
		return "", fmt.Errorf("%w: path must be canonical (no '..', no trailing or repeated '/')", ErrInvalidPath)
	}
	for _, prefix := range refusedPrefixes {
		if strings.HasPrefix(path, prefix) {
			return "", fmt.Errorf("%w: %s holds kernel and runtime state, not regular files", ErrRefusedPath, strings.TrimSuffix(prefix, "/"))
		}
	}
	for _, p := range refusedExact {
		if path == p {
			return "", fmt.Errorf("%w: %s carries credentials that must not be copied onto the platform", ErrRefusedPath, p)
		}
	}
	if strings.HasPrefix(path, "/etc/ssh/ssh_host_") && strings.HasSuffix(path, "_key") {
		return "", fmt.Errorf("%w: SSH host private keys must not be copied onto the platform", ErrRefusedPath)
	}
	base := filepath.Base(path)
	if strings.HasPrefix(base, "id_") && !strings.HasSuffix(base, ".pub") &&
		strings.Contains(path, "/.ssh/") {
		return "", fmt.Errorf("%w: SSH private keys must not be copied onto the platform", ErrRefusedPath)
	}
	for _, suffix := range refusedSuffixes {
		if strings.HasSuffix(path, suffix) {
			return "", fmt.Errorf("%w: %s files are private key material", ErrRefusedPath, suffix)
		}
	}
	return path, nil
}
