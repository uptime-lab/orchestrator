package node_file

import "testing"

// The rendered watchdog.conf that a successful apply reports back is recorded
// as a platform tracked file. That only works if the path policy lets the path
// in: protected (never untracked, never deleted on the device) but NOT refused.
func TestWatchdogConfPathIsTrackableButProtected(t *testing.T) {
	got, err := ValidateTrackPath(WatchdogConfPath)
	if err != nil {
		t.Fatalf("ValidateTrackPath(%q) refused the platform's own watchdog file: %v", WatchdogConfPath, err)
	}
	if got != WatchdogConfPath {
		t.Fatalf("ValidateTrackPath rewrote %q to %q", WatchdogConfPath, got)
	}
	if !IsProtected(WatchdogConfPath) {
		t.Fatalf("%q must be protected: the platform manages it", WatchdogConfPath)
	}
}
