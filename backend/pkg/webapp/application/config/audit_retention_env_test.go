package config

import "testing"

// The audit trail is kept a year unless the install says otherwise; 0 still
// means forever and is accepted, and an install can set its own default.
func TestAuditRetentionDefaultsToAYear(t *testing.T) {
	t.Setenv("AUDIT_EVENT_RETENTION_DAYS", "")
	cfg, err := ApplyEnvironment(getDefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Retention.AuditEventDays != 365 {
		t.Errorf("unset AUDIT_EVENT_RETENTION_DAYS = %d, want 365", cfg.Retention.AuditEventDays)
	}
	if cfg.Retention.NodeCommandDays != 0 {
		t.Errorf("command retention default = %d, want 0 (unchanged: forever)", cfg.Retention.NodeCommandDays)
	}

	for env, want := range map[string]int{"0": 0, "30": 30} {
		t.Setenv("AUDIT_EVENT_RETENTION_DAYS", env)
		cfg, err := ApplyEnvironment(getDefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Retention.AuditEventDays != want {
			t.Errorf("AUDIT_EVENT_RETENTION_DAYS=%s read as %d, want %d", env, cfg.Retention.AuditEventDays, want)
		}
	}
}
