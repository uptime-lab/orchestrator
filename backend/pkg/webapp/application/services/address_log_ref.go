package services

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// AddressLogRef is what a sign-in or sign-up log line carries in place of the
// address the caller typed: a short digest of the normalized address, enough
// to correlate attempts at one address and nothing more.
//
// The typed address is not logged because it is not yet known to be anyone's:
// a refused sign-in for an unknown address would otherwise write a stranger's
// mailbox — or a password pasted into the wrong field — into the log.
func AddressLogRef(email string) string {
	sum := sha256.Sum256([]byte(user.NormalizeEmail(email)))
	return hex.EncodeToString(sum[:6])
}
