package cdn

import (
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
)

// A signed link is a time-bounded capability to one account's object — a
// whole-disk capture among them. A shared cache told it may keep the body
// would keep serving it after the signature expired, so these responses must
// not be storable by anything between the install and the caller.
func TestServeFileIsNeverStoredByASharedCache(t *testing.T) {
	rec := serveFile(t, "acct/captures/disk.img.xz", "")
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") || !strings.Contains(cc, "private") || strings.Contains(cc, "public") {
		t.Fatalf("Cache-Control = %q, want private, no-store", cc)
	}
}

// The agent packages are public by design and fetched by every device on an
// install; they keep their shared-cache lifetime.
func TestAgentPackageStaysPubliclyCacheable(t *testing.T) {
	rec := serveAgentPackage(t, storage.TypeLocal)
	if cc := rec.Header().Get("Cache-Control"); !strings.HasPrefix(cc, "public") {
		t.Fatalf("Cache-Control = %q, want a public lifetime", cc)
	}
}
