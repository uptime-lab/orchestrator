package node_file

import (
	"errors"
	"testing"
)

func TestValidateTrackPath(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		wantErr error
	}{
		{"plain file", "/etc/hosts", nil},
		{"netplan with a psk is allowed with a warning, not refused", "/etc/netplan/50-cloud-init.yaml", nil},
		{"public key is fine", "/home/user/.ssh/id_ed25519.pub", nil},
		{"empty", "", ErrInvalidPath},
		{"relative", "etc/hosts", ErrInvalidPath},
		{"traversal", "/etc/../etc/hosts", ErrInvalidPath},
		{"trailing slash", "/etc/hosts/", ErrInvalidPath},
		{"doubled separator", "/etc//hosts", ErrInvalidPath},
		{"null byte", "/etc/ho\x00sts", ErrInvalidPath},
		{"proc", "/proc/kcore", ErrRefusedPath},
		{"sys", "/sys/kernel/debug/x", ErrRefusedPath},
		{"dev", "/dev/zero", ErrRefusedPath},
		{"run", "/run/agent.sock", ErrRefusedPath},
		{"shadow", "/etc/shadow", ErrRefusedPath},
		{"shadow backup", "/etc/shadow-", ErrRefusedPath},
		{"adoption file", "/etc/orchestrator/node-adoption.txt", ErrRefusedPath},
		{"agent binary", "/usr/bin/orchestrator-agent", ErrRefusedPath},
		{"ssh host key", "/etc/ssh/ssh_host_ed25519_key", ErrRefusedPath},
		{"ssh host public key is fine", "/etc/ssh/ssh_host_ed25519_key.pub", nil},
		{"user ssh private key", "/home/user/.ssh/id_ed25519", ErrRefusedPath},
		{"pem", "/opt/app/tls.pem", ErrRefusedPath},
		{"key", "/opt/app/tls.key", ErrRefusedPath},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateTrackPath(tc.path)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected %q to be allowed, got %v", tc.path, err)
				}
				if got != tc.path {
					t.Fatalf("expected the path back unchanged, got %q", got)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v for %q, got %v", tc.wantErr, tc.path, err)
			}
		})
	}
}

func TestLongPathRefused(t *testing.T) {
	long := "/etc/"
	for len(long) <= MaxPathLen {
		long += "a"
	}
	if _, err := ValidateTrackPath(long); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected a path longer than the column to be refused, got %v", err)
	}
}

func TestIsProtected(t *testing.T) {
	protected := []string{
		"/boot/firmware/config.txt",
		"/boot/config.txt",
		"/etc/watchdog.conf",
		"/etc/systemd/system.conf.d/10-orchestrator-watchdog.conf",
		"/etc/ssh/authorized_keys.d/orchestrator",
		"/etc/sudoers.d/90-orchestrator-ops",
		"/etc/machine-id",
	}
	for _, p := range protected {
		if !IsProtected(p) {
			t.Errorf("expected %q to be protected", p)
		}
	}

	unprotected := []string{"/etc/hosts", "/etc/ssh/sshd_config", "/opt/app/config.yaml"}
	for _, p := range unprotected {
		if IsProtected(p) {
			t.Errorf("expected %q not to be protected", p)
		}
	}
}

func TestIsPlatformPath(t *testing.T) {
	if !IsPlatformPath("/boot/firmware/config.txt") || !IsPlatformPath("/boot/config.txt") {
		t.Fatal("both config.txt paths are platform-owned")
	}
	// A protected file is not automatically a platform path: /etc/watchdog.conf
	// is written by a user-visible feature and reading it stays privileged.
	if IsPlatformPath("/etc/watchdog.conf") {
		t.Fatal("watchdog.conf is protected, not platform-read-level")
	}
}

// A backend without the decryption code serving encrypted rows would show the
// envelope in the editor, and Apply would write it into a boot config. This is
// the guard that makes that unwritable.
func TestLooksLikeStoredCiphertext(t *testing.T) {
	ciphertext := "v1:Abcd1234Abcd1234Abcd1234Abcd1234Abcd1234Abcd1234Abcd1234Abcd1234"
	if !LooksLikeStoredCiphertext(ciphertext) {
		t.Fatal("expected the at-rest envelope to be recognised")
	}
	if !LooksLikeStoredCiphertext("encv1:AAAABBBBCCCCDDDDEEEEFFFFGGGGHHHHIIIIJJJJ") {
		t.Fatal("expected the derived-secret envelope to be recognised")
	}

	realFiles := []string{
		"[all]\narm_64bit=1\n",
		"127.0.0.1 localhost\n",
		"v1: this is a config value with spaces\n",
		"version: 1\nkey: value\n",
		"",
	}
	for _, content := range realFiles {
		if LooksLikeStoredCiphertext(content) {
			t.Errorf("expected %q to be treated as real content", content)
		}
	}
}

func TestValidateTrackPathRefusesLegacyRunNames(t *testing.T) {
	// /var/run and /var/lock are the historical names for /run and /run/lock and
	// symlink into it on every distro we ship to. Refusing only the modern
	// spelling meant the same FIFO was refused as /run/x and accepted as
	// /var/run/x — and a FIFO blocks the agent's single command goroutine.
	for _, path := range []string{
		"/var/run/dbus/system_bus_socket",
		"/var/lock/subsys/thing",
		"/run/systemd/private",
	} {
		if _, err := ValidateTrackPath(path); err == nil {
			t.Errorf("%s must be refused", path)
		}
	}
}

func TestDefaultLimitsMatchTheConstants(t *testing.T) {
	// The constants remain the shipped defaults; the Limits struct is only how
	// an install overrides them. If the two drift, an unconfigured install and a
	// configured-to-the-default one behave differently.
	limits := DefaultLimits()
	if limits.VersionsPerPath != MaxVersionsPerPath ||
		limits.FilesPerNode != MaxTrackedPerNode ||
		limits.BytesPerNode != MaxTotalBytesPerNode {
		t.Errorf("DefaultLimits drifted from the constants: %+v", limits)
	}
	if limits.BytesPerAccount != 0 {
		t.Error("the per-account ceiling must start unlimited")
	}
}

// The device's access reconciler writes these names; a tracked-file write or
// delete of one would grant or revoke access the access entries decide.
var accessReconcilerFiles = []string{
	"/etc/sudoers.d/90-orchestrator-ops",
	"/etc/ssh/sshd_config.d/00-orchestrator-access.conf",
	"/etc/ssh/sshd_config.d/00-orchestrator.conf",
	"/etc/ssh/orchestrator_trusted_cas.pub",
	"/etc/ssh/orchestrator_ca.d/ca-1.pub",
	"/etc/ssh/auth_principals/ops",
	"/etc/ssh/authorized_keys.d/ops",
}

func TestTheAccessReconcilersFilesAreProtected(t *testing.T) {
	for _, p := range accessReconcilerFiles {
		if !IsProtected(p) {
			t.Errorf("%s is written by the access reconciler and must not be deleted from the device", p)
		}
	}
}

func TestWritesToTheAccessReconcilersFilesAreRefused(t *testing.T) {
	for _, p := range accessReconcilerFiles {
		if err := ValidateWritePath(p); !errors.Is(err, ErrAccessManagedPath) {
			t.Errorf("a write to %s was not refused: %v", p, err)
		}
	}
	for _, p := range []string{"/etc/sudoers.d/ops", "/etc/ssh/sshd_config.d/50-local.conf", "/etc/hosts"} {
		if err := ValidateWritePath(p); err != nil {
			t.Errorf("%s was refused: %v", p, err)
		}
	}
}
