package ssh_key

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_update"
)

func requireBadRequest(t *testing.T, what string, err error) {
	t.Helper()
	var refusal *app_errors.RequestRefusalError
	if !errors.As(err, &refusal) || refusal.HTTPStatus() != http.StatusBadRequest {
		t.Errorf("%s: err = %v, want a typed 400", what, err)
	}
}

// Content that is not a public key, and a key type outside the known set,
// are the request's fault: the domain already words the refusal, and it
// reached the operator as a 500 "failed to create SSH key".
func TestMalformedKeyInputIsARequestRefusal(t *testing.T) {
	svc, _, _ := auditKeyService()
	auth := commands.NewAuthContext(uuid.New(), uuid.New())
	ctx := context.Background()

	_, err := svc.Create(ctx, ssh_key_create.SSHKeyCreateCommand{ID: uuid.New(), Alias: "x", KeyContent: "not a key", AuthContext: auth})
	requireBadRequest(t, "unparseable content", err)

	_, err = svc.Create(ctx, ssh_key_create.SSHKeyCreateCommand{ID: uuid.New(), Alias: "x", KeyContent: auditTestPublicKey, KeyType: "public", AuthContext: auth})
	requireBadRequest(t, "unknown key type", err)

	key, err := svc.Create(ctx, ssh_key_create.SSHKeyCreateCommand{ID: uuid.New(), Alias: "x", KeyContent: auditTestPublicKey, AuthContext: auth})
	if err != nil {
		t.Fatalf("a valid key: %v", err)
	}
	garbage := "garbage"
	_, err = svc.Update(ctx, ssh_key_update.SSHKeyUpdateCommand{ID: key.ID, KeyContent: &garbage, AuthContext: auth})
	requireBadRequest(t, "unparseable replacement content", err)
}

// A generated connection keypair keeps its private key, which SSH-adopt
// authenticates with. Replacing only the public half used to succeed and
// leave a pair that no longer matches, so every adoption with it failed.
func TestAGeneratedKeypairsPublicKeyCannotBeReplaced(t *testing.T) {
	svc, store, _ := auditKeyService()
	auth := commands.NewAuthContext(uuid.New(), uuid.New())
	ctx := context.Background()

	key, err := svc.Create(ctx, ssh_key_create.SSHKeyCreateCommand{ID: uuid.New(), Alias: "conn", KeyContent: auditTestPublicKey, AuthContext: auth})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	store.byID[key.ID].HasPrivateKey = true

	replacement := auditTestPublicKey
	_, err = svc.Update(ctx, ssh_key_update.SSHKeyUpdateCommand{ID: key.ID, KeyContent: &replacement, AuthContext: auth})
	requireBadRequest(t, "replacing a generated keypair's public key", err)

	alias := "renamed"
	if _, err := svc.Update(ctx, ssh_key_update.SSHKeyUpdateCommand{ID: key.ID, Alias: &alias, AuthContext: auth}); err != nil {
		t.Fatalf("renaming a generated keypair: %v", err)
	}
}
