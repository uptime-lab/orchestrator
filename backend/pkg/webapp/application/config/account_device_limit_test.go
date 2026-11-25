package config

import (
	"strings"
	"testing"
)

// ACCOUNT_DEVICE_LIMIT is off by default, so a self-hosted install does not
// change; a value that is not a non-negative integer leaves it off and is
// named by an advisory, never a refused boot.
func TestAccountDeviceLimitFromEnvironment(t *testing.T) {
	c := getDefaultConfig()
	if c.Entitlement.EffectiveAccountDeviceLimit() != 0 {
		t.Fatal("a per-account device limit is on by default")
	}
	for _, a := range c.Advisories() {
		if strings.Contains(a, "ACCOUNT_DEVICE_LIMIT") {
			t.Errorf("an advisory with the limit unset: %s", a)
		}
	}

	t.Setenv("ACCOUNT_DEVICE_LIMIT", "5")
	t.Setenv("ACCOUNT_DEVICE_LIMIT_CONTACT", "sales@example.com")
	if _, err := ApplyEnvironment(c); err != nil {
		t.Fatal(err)
	}
	if c.Entitlement.EffectiveAccountDeviceLimit() != 5 || c.Entitlement.EffectiveAccountDeviceLimitContact() != "sales@example.com" {
		t.Errorf("limit %d contact %q", c.Entitlement.EffectiveAccountDeviceLimit(), c.Entitlement.EffectiveAccountDeviceLimitContact())
	}

	t.Setenv("ACCOUNT_DEVICE_LIMIT", "ten")
	if _, err := ApplyEnvironment(c); err != nil {
		t.Fatal(err)
	}
	if c.Entitlement.EffectiveAccountDeviceLimit() != 0 {
		t.Error("an unparseable limit was enforced")
	}
	if !advised(c, "ACCOUNT_DEVICE_LIMIT is not a non-negative integer") {
		t.Error("an unparseable limit was not named by an advisory")
	}
	for _, p := range c.Problems() {
		if strings.Contains(p, "ACCOUNT_DEVICE_LIMIT") {
			t.Errorf("the per-account limit became a boot refusal: %s", p)
		}
	}
}

// The contact is shown to every tenant: one line, bounded, or it is dropped.
func TestAccountDeviceLimitContactIsOneBoundedLine(t *testing.T) {
	e := EntitlementConfig{AccountDeviceLimit: 5, AccountDeviceLimitContact: "sales@example.com\nInjected: yes"}
	if e.EffectiveAccountDeviceLimitContact() != "" {
		t.Error("a multi-line contact was kept")
	}
	if len(e.accountLimitAdvisories()) != 2 {
		t.Errorf("advisories = %v, want the invalid contact and the missing contact", e.accountLimitAdvisories())
	}
	e.AccountDeviceLimitContact = strings.Repeat("a", 255)
	if e.EffectiveAccountDeviceLimitContact() != "" {
		t.Error("an over-long contact was kept")
	}
}

func advised(c *Config, s string) bool {
	for _, a := range c.Advisories() {
		if strings.Contains(a, s) {
			return true
		}
	}
	return false
}
