package storage

import "testing"

// What this pins, in full, because a table test with no reason gets
// "simplified" back into the bug:
//
// Asking `GetStorageType() == "local"` where the question is "can the CDN proxy
// this" breaks the route registration: switching an install to S3 would
// unregister /cdn/agent-versions, /cdn/agent/:v/:arch/:type and /cdn/*, so
// install.sh, every update_agent and every install_os image fetch would return
// 404 — a fleet-wide outage caused by a configuration change, with an error
// that reads like a missing file rather than a missing route.
func TestServedThroughCDN(t *testing.T) {
	cases := map[string]bool{
		TypeLocal: true,
		TypeS3:    true,
		// GCP hands out provider signed URLs and is not exercised by the test
		// suite, so it keeps its behaviour rather than being changed untested.
		TypeGCP: false,
		"":      false,
		"minio": false,
	}
	for storageType, want := range cases {
		if got := ServedThroughCDN(storageType); got != want {
			t.Errorf("ServedThroughCDN(%q) = %v, want %v", storageType, got, want)
		}
	}
}

// If a new backend is added and this fails, the fix is to decide deliberately
// whether the CDN proxies it — not to delete the assertion.
func TestS3IsServedThroughTheCDNSoItsRoutesStayRegistered(t *testing.T) {
	if !ServedThroughCDN(TypeS3) {
		t.Fatal("S3 is not served through the CDN: the /cdn/agent/... routes would not be registered, " +
			"which takes install.sh and update_agent down for the whole fleet")
	}
}
