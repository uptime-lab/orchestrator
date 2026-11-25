package config

import "testing"

// The default has to be the SAFE direction: an install that says nothing about a
// reader is assumed to have none, and gets the startup warning.
//
// The backend records instance anomalies unconditionally by design, so on an
// install with no operator console reading them, they accumulate in a table
// nothing reads. The backend cannot derive that: nothing in this process can
// see whether a reader exists. So it is declared, and silence means "no
// reader" rather than "probably fine".
func TestAnInstallThatSaysNothingHasNoAnomalyReader(t *testing.T) {
	cfg := getDefaultConfig()
	if cfg.AnomaliesAreRead() {
		t.Fatal("the default claims something reads instance_anomalies; silence must mean the opposite")
	}
}

func TestNamingTheAdminConsoleSilencesTheWarning(t *testing.T) {
	cfg := getDefaultConfig()
	for _, v := range []string{"admin-console", "Admin-Console", "  admin-console  "} {
		cfg.Monitoring.AnomalyReader = v
		if !cfg.AnomaliesAreRead() {
			t.Errorf("%q was not recognised as a reader", v)
		}
	}
}

// Anything else is not a reader, including values that look like one.
func TestOnlyTheKnownReaderCounts(t *testing.T) {
	cfg := getDefaultConfig()
	for _, v := range []string{"", "none", "yes", "true", "grafana", "admin"} {
		cfg.Monitoring.AnomalyReader = v
		if cfg.AnomaliesAreRead() {
			t.Errorf("%q was treated as a reader; only the admin console consumes these rows", v)
		}
	}
}

// "none" is a statement, not a reader: the rows are still unread, and only
// that exact word (not silence, not a near miss) silences the warning.
func TestNoneDeclaresThatNothingReads(t *testing.T) {
	cfg := getDefaultConfig()
	if cfg.AnomalyReaderDeclaredAbsent() {
		t.Fatal("silence was taken as a declaration that nothing reads instance_anomalies")
	}
	for _, v := range []string{"none", "None", "  none  "} {
		cfg.Monitoring.AnomalyReader = v
		if !cfg.AnomalyReaderDeclaredAbsent() {
			t.Errorf("%q was not recognised as a declaration", v)
		}
		if cfg.AnomaliesAreRead() {
			t.Errorf("%q was treated as a reader", v)
		}
	}
	for _, v := range []string{"", "no", "admin-console", "nobody"} {
		cfg.Monitoring.AnomalyReader = v
		if cfg.AnomalyReaderDeclaredAbsent() {
			t.Errorf("%q was taken as a declaration that nothing reads", v)
		}
	}
}
