package results

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_deletion_request"
)

// The notice is shown verbatim by the SPA. Only an install with something
// that performs the erasure may say the operator will erase the account; a
// self-hosted install must say the request is recorded and nothing more.
func TestDeletionNoticeFollowsTheErasureExecutor(t *testing.T) {
	req := &account_deletion_request.Request{ID: uuid.New(), AccountID: uuid.New(), RequestedBy: uuid.New(), RequestedAt: time.Now()}

	hosted := NewAccountDeletionRequestResult(req, true).Notice
	if !strings.Contains(hosted, "will erase the account") {
		t.Fatalf("hosted notice does not promise the erasure its console performs: %q", hosted)
	}

	self := NewAccountDeletionRequestResult(req, false).Notice
	if strings.Contains(self, "will erase") {
		t.Fatalf("self-hosted notice promises an erasure nothing performs: %q", self)
	}
	for _, must := range []string{"NOT deleted", "operator of this install", "export"} {
		if !strings.Contains(self, must) {
			t.Fatalf("self-hosted notice lacks %q: %q", must, self)
		}
	}
}
