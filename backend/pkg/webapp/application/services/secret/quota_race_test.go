package secret

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_create"
)

// The per-account secret cap has to BIND under concurrency, because the workflow
// engine drives creation hundreds wide.
//
// server.attribute_upsert runs against up to
// WORKFLOW_MAX_RUNNING_NODES_PER_ACCOUNT execution nodes concurrently, and node
// attributes ARE secrets (attr:<nodeID>:<key>), so a single fan-out had every
// goroutine reading the same count before any of them inserted. The pre-check
// stays for the early, well-worded refusal; the cap is enforced in the write.
//
// The fake serialises its own map, so what this test measures is whether the
// service asks the repository to enforce the cap at all — with the old
// read-then-write it does not, and every creation lands.
func TestTheSecretCapBindsWhenCreatesRace(t *testing.T) {
	svc, repo, _ := newTestService()
	svc.SetMaxSecretsPerAccount(3)
	accountID := uuid.New()

	const attempts = 25

	// Every caller reads the count before any of them writes. That is the
	// interleaving a fan-out produces and the one a scheduler will not reliably
	// give you, so it is forced rather than hoped for — without it this test
	// passes against a cap that binds nothing.
	var arrived sync.WaitGroup
	arrived.Add(attempts)
	repo.countGate = func() {
		arrived.Done()
		arrived.Wait()
	}

	var wg sync.WaitGroup
	errs := make([]error, attempts)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := svc.Create(context.Background(), secret_create.SecretCreateCommand{
				Name:        "attr-" + uuid.New().String(),
				Kind:        "env",
				Key:         "K",
				Value:       "v",
				AuthContext: commands.NewAuthContext(accountID, uuid.New()),
			})
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	created := 0
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, app_errors.ErrSecretLimitReached):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if created > 3 {
		t.Fatalf("%d secrets were created against a cap of 3; the cap is a read-then-write and bounds nothing under a fan-out", created)
	}
	if created == 0 {
		t.Fatal("nothing was created at all; the cap must admit up to its limit")
	}
}
