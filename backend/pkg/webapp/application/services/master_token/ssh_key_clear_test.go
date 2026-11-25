package master_token

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/master_token_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/master_token"
)

// "SSH key: None" must stop a token installing its key. An empty string
// clears it; before, the field was a UUID pointer, null meant unchanged and
// "" was refused, so the key could not be taken off a token at all.
func TestMasterTokenSSHKeyCanBeCleared(t *testing.T) {
	repo := newAuditTokenRepo()
	recorder, _ := audittest.New()
	svc := NewService(repo, nil, acceptAllOURepo{}, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	accountID := uuid.New()
	keyID := uuid.New()
	tok := &master_token.MasterToken{ID: uuid.New(), AccountID: accountID, Name: "t", SSHKeyID: &keyID}
	repo.byID[tok.ID] = tok

	clear := ""
	if _, err := svc.Update(context.Background(), master_token_update.MasterTokenUpdateCommand{
		ID: tok.ID, SSHKeyID: &clear, AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := repo.byID[tok.ID].SSHKeyID; got != nil {
		t.Fatalf("ssh key = %v after clearing, want none", got)
	}
}
