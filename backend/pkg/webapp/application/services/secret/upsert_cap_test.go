package secret

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// staleCountRepo answers the service's pre-check with a count read before the
// other writers of a fan-out inserted, which is what every goroutine of a
// playbook fan-out sees. Only the write itself can then hold the cap.
type staleCountRepo struct {
	*fakeSecretRepo
}

func (staleCountRepo) CountAll(context.Context, uuid.UUID) (int64, error) { return 0, nil }

// fullAccount returns a service whose account already holds cap secrets, with
// a pre-check that cannot see them.
func fullAccount(t *testing.T, cap int) (*Service, *fakeSecretRepo, uuid.UUID) {
	t.Helper()
	repo := newFakeSecretRepo()
	accountID := uuid.New()
	for i := 0; i < cap; i++ {
		sec, err := secret.NewSecret(accountID, "existing-"+uuid.NewString(), secret.KindOpaque, "", "v", true)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Create(context.Background(), sec); err != nil {
			t.Fatal(err)
		}
	}
	recorder, _ := audittest.New()
	svc := NewService(staleCountRepo{repo}, &fakeAttachmentRepo{}, fakeNodeRepoForSecret{}, fakeOURepoForSecret{}, recorder, slog.Default())
	svc.SetMaxSecretsPerAccount(cap)
	return svc, repo, accountID
}

func countSecrets(repo *fakeSecretRepo, accountID uuid.UUID) int {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	n := 0
	for _, s := range repo.byID {
		if s.AccountID == accountID {
			n++
		}
	}
	return n
}

// A playbook's Save secret block creates secrets from many nodes at once, so
// the per-account cap must bind in the write, as it does for the ordinary
// create path, not only in the read before it.
func TestUpsertForNodeCreatesUnderTheCap(t *testing.T) {
	svc, repo, accountID := fullAccount(t, 3)
	err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, uuid.New(),
		"captured", secret.KindOpaque, "", "tok", secret.UpsertOptions{})
	if !errors.Is(err, app_errors.ErrSecretLimitReached) {
		t.Fatalf("upsert error = %v, want ErrSecretLimitReached", err)
	}
	if got := countSecrets(repo, accountID); got != 3 {
		t.Errorf("account holds %d secrets against a cap of 3", got)
	}
}

// Node attributes are secrets and the attribute block fans out the same way.
func TestUpsertNodeAttributeCreatesUnderTheCap(t *testing.T) {
	svc, repo, accountID := fullAccount(t, 3)
	_, err := svc.UpsertNodeAttribute(context.Background(), audit.SystemActor(accountID), accountID, uuid.New(),
		"ci_token", "tok", false)
	if !errors.Is(err, app_errors.ErrSecretLimitReached) {
		t.Fatalf("attribute upsert error = %v, want ErrSecretLimitReached", err)
	}
	if got := countSecrets(repo, accountID); got != 3 {
		t.Errorf("account holds %d secrets against a cap of 3", got)
	}
}
