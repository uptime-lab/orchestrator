package organizational_unit

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

type unreadableSecretRepo struct {
	repository.SecretRepository
	secrets []secret.Secret
}

func (r *unreadableSecretRepo) GetByIDs(context.Context, uuid.UUID, []uuid.UUID) ([]secret.Secret, error) {
	return r.secrets, nil
}

// The repository degrades an undecryptable value to empty + a flag so the
// Secrets list keeps rendering. This resolution is the other side of that: it
// feeds the agent env, {{secrets.*}} and the effective-secrets view, so a
// referenced secret whose value cannot be read must fail the caller instead of
// delivering an empty credential.
func TestResolveRefusesAReferencedUnreadableSecret(t *testing.T) {
	readable, unreadable := uuid.New(), uuid.New()
	svc := &Service{secretRepo: &unreadableSecretRepo{secrets: []secret.Secret{
		{ID: readable, Name: "fine", Kind: secret.KindEnv, Key: "OK", Value: "v"},
		{ID: unreadable, Name: "orphan", Kind: secret.KindEnv, Key: "BROKEN", ValueUnreadable: true},
	}}}

	levels := []secretLevel{{name: "node", refs: []secretRef{
		{secretID: readable, via: "direct"},
		{secretID: unreadable, via: "direct"},
	}}}

	_, err := svc.loadReferencedSecrets(context.Background(), uuid.New(), levels)
	if !errors.Is(err, app_errors.ErrSecretValueUnreadable) {
		t.Fatalf("got %v, want ErrSecretValueUnreadable", err)
	}
}

// An unreadable row that nothing is attached to must not break resolution for
// the nodes that do not reference it — that is the whole point of degrading the
// read rather than aborting it.
func TestResolveIgnoresAnUnreadableSecretNothingReferences(t *testing.T) {
	readable := uuid.New()
	svc := &Service{secretRepo: &unreadableSecretRepo{secrets: []secret.Secret{
		{ID: readable, Name: "fine", Kind: secret.KindEnv, Key: "OK", Value: "v"},
	}}}

	levels := []secretLevel{{name: "node", refs: []secretRef{{secretID: readable, via: "direct"}}}}

	byID, err := svc.loadReferencedSecrets(context.Background(), uuid.New(), levels)
	if err != nil {
		t.Fatalf("loadReferencedSecrets() = %v, want nil", err)
	}
	if got := byID[readable].Value; got != "v" {
		t.Errorf("referenced secret value = %q, want %q", got, "v")
	}
}
