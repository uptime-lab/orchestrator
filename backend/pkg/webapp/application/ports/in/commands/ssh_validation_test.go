package commands

import (
	"strings"
	"testing"
)

const validFingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// The fingerprint must be exactly what the adapter emits. A prefix check alone
// let a request carry megabytes into the job row, the audit detail and the log.
func TestValidateHostKeyFingerprintAcceptsOnlyTheAdapterShape(t *testing.T) {
	if err := ValidateHostKeyFingerprint("f", ""); err != nil {
		t.Fatalf("empty (no pin) must pass: %v", err)
	}
	if err := ValidateHostKeyFingerprint("f", validFingerprint); err != nil {
		t.Fatalf("a well-formed fingerprint must pass: %v", err)
	}
	if err := ValidateHostKeyFingerprint("f", "SHA256:Ikirm3ivRW3+GhmhvdKWacc/MOu8b8yfH1FhZGoFkVU"); err != nil {
		t.Fatalf("a real OpenSSH fingerprint must pass: %v", err)
	}
	for name, bad := range map[string]string{
		"prefix only":     "SHA256:",
		"too short":       "SHA256:AAAA",
		"too long":        validFingerprint + "A",
		"padded base64":   "SHA256:" + strings.Repeat("A", 42) + "=",
		"wrong algorithm": "MD5:" + strings.Repeat("a", 43),
		"whitespace":      " " + validFingerprint,
		"newline inside":  "SHA256:AAAA\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"megabyte":        "SHA256:" + strings.Repeat("A", 1<<20),
	} {
		if err := ValidateHostKeyFingerprint("f", bad); err == nil {
			t.Errorf("%s: %q was accepted", name, truncate(bad))
		}
	}
}

// The size bounds apply to the value as sent: padding a host or username with
// whitespace must not slip past them, and is refused rather than trimmed,
// because the callers dial the raw string.
func TestValidateSSHTargetRefusesPaddingAndMeasuresTheRawValue(t *testing.T) {
	if err := ValidateSSHTarget("pi.lan", "pi"); err != nil {
		t.Fatalf("plain target must pass: %v", err)
	}
	if err := ValidateSSHTarget("fe80::1%eth0", "pi"); err != nil {
		t.Fatalf("an IPv6 literal with a zone must pass: %v", err)
	}
	pad := strings.Repeat(" ", 4096)
	for name, tc := range map[string][2]string{
		"host padded":       {pad + "pi.lan" + pad, "pi"},
		"username padded":   {"pi.lan", pad + "pi"},
		"host trailing tab": {"pi.lan\t", "pi"},
		"username newline":  {"pi.lan", "pi\n"},
		"host too long":     {strings.Repeat("a", MaxSSHHostBytes+1), "pi"},
		"username too long": {"pi.lan", strings.Repeat("u", MaxSSHUsernameBytes+1)},
		"host with slash":   {"pi.lan/x", "pi"},
		"empty host":        {"", "pi"},
		"empty username":    {"pi.lan", ""},
	} {
		if err := ValidateSSHTarget(tc[0], tc[1]); err == nil {
			t.Errorf("%s: host %q username %q was accepted", name, truncate(tc[0]), truncate(tc[1]))
		}
	}
}

func TestValidateSSHCredentialSizes(t *testing.T) {
	if err := ValidateSSHCredentialSizes(strings.Repeat("p", MaxSSHPasswordBytes), ""); err != nil {
		t.Fatalf("a password at the bound must pass: %v", err)
	}
	if err := ValidateSSHCredentialSizes(strings.Repeat("p", MaxSSHPasswordBytes+1), ""); err == nil {
		t.Error("a password over the bound was accepted")
	}
	if err := ValidateSSHCredentialSizes("", strings.Repeat("p", MaxSSHPasswordBytes+1)); err == nil {
		t.Error("a sudo password over the bound was accepted")
	}
}

func truncate(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
