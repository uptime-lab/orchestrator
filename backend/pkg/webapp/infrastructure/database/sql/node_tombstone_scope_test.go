package sql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Every account-taking statement on deleted-node records names the caller's
// account in its WHERE, so a record of one account is invisible to, and
// untouchable from, every other. Rendered without a database: the assertion is
// on the statements issued.
func TestDeletedNodeStatementsAreScopedByTheCallersAccount(t *testing.T) {
	account, id := uuid.New(), uuid.New()
	ctx := context.Background()
	cases := []struct {
		name string
		run  func(r *NodeTombstoneRepository)
	}{
		{"ListByAccountID", func(r *NodeTombstoneRepository) { _, _ = r.ListByAccountID(ctx, account) }},
		{"GetByID", func(r *NodeTombstoneRepository) { _, _ = r.GetByID(ctx, account, id) }},
		{"MarkRestored", func(r *NodeTombstoneRepository) { _ = r.MarkRestored(ctx, account, id) }},
		{"MarkIdentityReleased", func(r *NodeTombstoneRepository) { _ = r.MarkIdentityReleased(ctx, account, id, time.Now()) }},
		{"MarkRefused", func(r *NodeTombstoneRepository) { _ = r.MarkRefused(ctx, account, id, time.Now(), time.Minute) }},
		{"Delete", func(r *NodeTombstoneRepository) { _ = r.Delete(ctx, account, id) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, capture := capturingDryRunDB(t)
			tc.run(NewNodeTombstoneRepository(db, passthroughEncryptor{}))
			if len(capture.stmt) == 0 {
				t.Fatal("no statement rendered")
			}
			for _, stmt := range capture.stmt {
				if !strings.Contains(stmt, "account_id = '"+account.String()+"'") {
					t.Errorf("statement is not scoped by the caller's account: %s", stmt)
				}
			}
		})
	}

	t.Run("SerialHoldersInAccount", func(t *testing.T) {
		db, capture := capturingDryRunDB(t)
		_, _ = NewNodeRepository(db, passthroughEncryptor{}).SerialHoldersInAccount(ctx, account, []string{"10000000abcd0001"})
		if len(capture.stmt) != 1 {
			t.Fatalf("rendered %d statements, want 1: %q", len(capture.stmt), capture.stmt)
		}
		stmt := capture.stmt[0]
		if !strings.Contains(stmt, "account_id = '"+account.String()+"'") {
			t.Errorf("the holder lookup is not scoped by the caller's account: %s", stmt)
		}
		if strings.Contains(stmt, "unique_token") || strings.Contains(stmt, "default_password") || strings.Contains(stmt, "SELECT *") {
			t.Errorf("the holder lookup reads more than id and name: %s", stmt)
		}
	})
}
