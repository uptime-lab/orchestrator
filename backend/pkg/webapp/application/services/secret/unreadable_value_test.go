package secret

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// storeUnreadable seeds a row the repository can no longer decrypt: the value
// is gone from the domain object but the ciphertext is still in the column.
func storeUnreadable(t *testing.T, repo *fakeSecretRepo, accountID uuid.UUID, name string) *secret.Secret {
	t.Helper()
	sec, err := secret.NewSecret(accountID, name, secret.KindOpaque, "", "", true)
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	sec.ValueUnreadable = true
	if err := repo.Create(context.Background(), sec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return sec
}

// Update writes the whole row back, so an edit that does not carry a value
// would encrypt "" over ciphertext a restored key could still recover.
func TestUpdateWithoutAValueRefusesAnUnreadableSecret(t *testing.T) {
	svc, repo, _ := newTestService()
	accountID := uuid.New()
	sec := storeUnreadable(t, repo, accountID, "orphan")

	rename := "renamed"
	_, err := svc.Update(context.Background(), secret_update.SecretUpdateCommand{
		ID:          sec.ID,
		Name:        &rename,
		AuthContext: commands.AuthContext{AccountID: accountID, UserID: uuid.New()},
	})
	if !errors.Is(err, app_errors.ErrSecretValueUnreadable) {
		t.Fatalf("got %v, want ErrSecretValueUnreadable", err)
	}
	stored, _ := repo.GetByID(context.Background(), accountID, sec.ID)
	if stored.Name != "orphan" || !stored.ValueUnreadable {
		t.Errorf("the refused update still changed the row: %+v", stored)
	}
}

// Re-setting the value is the repair path and must keep working.
func TestUpdateWithANewValueRepairsAnUnreadableSecret(t *testing.T) {
	svc, repo, _ := newTestService()
	accountID := uuid.New()
	sec := storeUnreadable(t, repo, accountID, "orphan")

	value := "re-set-by-the-operator"
	updated, err := svc.Update(context.Background(), secret_update.SecretUpdateCommand{
		ID:          sec.ID,
		Value:       &value,
		AuthContext: commands.AuthContext{AccountID: accountID, UserID: uuid.New()},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ValueUnreadable {
		t.Error("the secret is still flagged unreadable after a value was supplied")
	}
	stored, _ := repo.GetByID(context.Background(), accountID, sec.ID)
	if stored.Value != value {
		t.Errorf("stored value = %q, want %q", stored.Value, value)
	}
}
