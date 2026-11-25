package main

import "testing"

// A build without the PLATFORM_VERSION stamp reports no version; a stamped
// one reports it as given, surrounding whitespace aside.
func TestBuildVersion(t *testing.T) {
	saved := stampedVersion
	t.Cleanup(func() { stampedVersion = saved })

	stampedVersion = ""
	if got := buildVersion(); got != "" {
		t.Errorf("buildVersion() unstamped = %q, want empty", got)
	}
	stampedVersion = " 0.1.1\n"
	if got := buildVersion(); got != "0.1.1" {
		t.Errorf("buildVersion() = %q, want 0.1.1", got)
	}
}
