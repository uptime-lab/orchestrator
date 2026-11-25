package secret

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_delete"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/secret_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

func requireBadRequest(t *testing.T, err error, want error) {
	t.Helper()
	var refusal *app_errors.RequestRefusalError
	if !errors.As(err, &refusal) || refusal.HTTPStatus() != http.StatusBadRequest {
		t.Fatalf("err = %v, want a typed 400", err)
	}
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v inside", err, want)
	}
}

// A name a {{secrets.<name>}} reference cannot reach, a name over the column
// width and the reserved node-attribute prefix are refused at create.
func TestCreateRefusesANameNoReferenceCanReach(t *testing.T) {
	svc, _, _ := newTestService()
	auth := commands.NewAuthContext(uuid.New(), uuid.New())
	cases := map[string]error{
		"api}token":                       secret.ErrInvalidName,
		strings.Repeat("n", 256):          secret.ErrNameTooLong,
		"attr:" + uuid.NewString() + ":X": secret.ErrReservedName,
	}
	for name, want := range cases {
		_, err := svc.Create(context.Background(), secret_create.SecretCreateCommand{
			Name: name, Kind: secret.KindOpaque, Value: "v", AuthContext: auth,
		})
		requireBadRequest(t, err, want)
	}
	// What the resolver does reach stays accepted, spaces and dots included.
	for _, name := range []string{"Registry credentials", "ci.token-1", "bad name!"} {
		if _, err := svc.Create(context.Background(), secret_create.SecretCreateCommand{
			Name: name, Kind: secret.KindOpaque, Value: "v", AuthContext: auth,
		}); err != nil {
			t.Errorf("create %q: %v", name, err)
		}
	}
}

// A stored name that predates the rule keeps working: re-sending it on an
// edit is not a rename, while renaming TO such a name is refused.
func TestUpdateChecksTheNameOnlyWhenItChanges(t *testing.T) {
	svc, repo, _ := newTestService()
	accountID := uuid.New()
	legacy, _ := secret.NewSecret(accountID, "legacy}name", secret.KindOpaque, "", "v", true)
	if err := repo.Create(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	auth := commands.NewAuthContext(accountID, uuid.New())
	same, value := legacy.Name, "rotated"
	if _, err := svc.Update(context.Background(), secret_update.SecretUpdateCommand{
		ID: legacy.ID, Name: &same, Value: &value, AuthContext: auth,
	}); err != nil {
		t.Fatalf("an edit re-sending the stored name: %v", err)
	}
	bad := "other}name"
	_, err := svc.Update(context.Background(), secret_update.SecretUpdateCommand{ID: legacy.ID, Name: &bad, AuthContext: auth})
	requireBadRequest(t, err, secret.ErrInvalidName)
}

// The agent refuses an env set containing a value with a line break, and that
// refusal fails every provisioning of the node. The API refuses the value
// instead, for env secrets (create and update) and node attributes alike; an
// opaque value may carry one (a PEM key).
func TestEnvValuesWithALineBreakAreRefused(t *testing.T) {
	svc, repo, _ := newTestService()
	ctx := context.Background()
	accountID, nodeID := uuid.New(), uuid.New()
	auth := commands.NewAuthContext(accountID, uuid.New())

	for _, v := range []string{"a\nb", "a\rb"} {
		_, err := svc.Create(ctx, secret_create.SecretCreateCommand{Name: "env-" + uuid.NewString(), Kind: secret.KindEnv, Key: "K", Value: v, AuthContext: auth})
		requireBadRequest(t, err, secret.ErrValueLineBreak)

		_, err = svc.UpsertNodeAttribute(ctx, audit.UserActor(accountID, uuid.New()), accountID, nodeID, "QA_NL", v, false)
		requireBadRequest(t, err, secret.ErrValueLineBreak)
	}

	env, _ := secret.NewSecret(accountID, "env-ok", secret.KindEnv, "K", "single", false)
	if err := repo.Create(ctx, env); err != nil {
		t.Fatal(err)
	}
	multi := "a\nb"
	_, err := svc.Update(ctx, secret_update.SecretUpdateCommand{ID: env.ID, Value: &multi, AuthContext: auth})
	requireBadRequest(t, err, secret.ErrValueLineBreak)

	if _, err := svc.Create(ctx, secret_create.SecretCreateCommand{
		Name: "pem", Kind: secret.KindOpaque, Value: "-----BEGIN-----\nabc\n-----END-----", AuthContext: auth,
	}); err != nil {
		t.Errorf("an opaque multi-line value: %v", err)
	}
}

// A value over the bound is refused for every kind; one at the bound is kept.
func TestSecretValueSizeIsBounded(t *testing.T) {
	svc, _, _ := newTestService()
	auth := commands.NewAuthContext(uuid.New(), uuid.New())
	_, err := svc.Create(context.Background(), secret_create.SecretCreateCommand{
		Name: "big", Kind: secret.KindOpaque, Value: strings.Repeat("x", secret.MaxValueBytes+1), AuthContext: auth,
	})
	requireBadRequest(t, err, secret.ErrValueTooLarge)
	if _, err := svc.Create(context.Background(), secret_create.SecretCreateCommand{
		Name: "fits", Kind: secret.KindOpaque, Value: strings.Repeat("x", secret.MaxValueBytes), AuthContext: auth,
	}); err != nil {
		t.Errorf("a value at the bound: %v", err)
	}
}

// The agent refuses an env set whose key is not a shell identifier, which
// fails every provisioning of the node. The API refuses such a key for env
// secrets at create, at a key change and in the workflow upsert; a
// credential's username and an opaque label are not variable names and stay
// free-form.
func TestEnvKeysTheAgentRefusesAreRefused(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()
	accountID, nodeID := uuid.New(), uuid.New()
	auth := commands.NewAuthContext(accountID, uuid.New())

	for _, key := range []string{"", "1ABC", "A-B", "A B", "A=B", "A.B", "\u00c9T\u00c9", "A\nB"} {
		_, err := svc.Create(ctx, secret_create.SecretCreateCommand{Name: "env-" + uuid.NewString(), Kind: secret.KindEnv, Key: key, Value: "v", AuthContext: auth})
		requireBadRequest(t, err, secret.ErrInvalidEnvKey)

		err = svc.UpsertForNode(ctx, audit.SystemActor(accountID), accountID, nodeID, "up-"+uuid.NewString(), secret.KindEnv, key, "v", secret.UpsertOptions{})
		requireBadRequest(t, err, secret.ErrInvalidEnvKey)
	}
	for _, key := range []string{"A", "_A", "a1_B2", "API_TOKEN"} {
		if _, err := svc.Create(ctx, secret_create.SecretCreateCommand{Name: "env-" + uuid.NewString(), Kind: secret.KindEnv, Key: key, Value: "v", AuthContext: auth}); err != nil {
			t.Errorf("create env key %q: %v", key, err)
		}
	}
	if _, err := svc.Create(ctx, secret_create.SecretCreateCommand{Name: "cred", Kind: secret.KindCredential, Key: "deploy user", Value: "v", AuthContext: auth}); err != nil {
		t.Errorf("a credential username is not a variable name: %v", err)
	}
	if _, err := svc.Create(ctx, secret_create.SecretCreateCommand{Name: "label", Kind: secret.KindOpaque, Key: "", Value: "v", AuthContext: auth}); err != nil {
		t.Errorf("an opaque entry without a label: %v", err)
	}
}

// A stored env key that predates the rule keeps the entry usable: it reads,
// takes a value rotation, takes an edit that re-sends the stored key and is
// deletable; changing the key TO an invalid one is refused.
func TestStoredInvalidEnvKeyStaysReadableEditableAndDeletable(t *testing.T) {
	svc, repo, _ := newTestService()
	ctx := context.Background()
	accountID := uuid.New()
	auth := commands.NewAuthContext(accountID, uuid.New())
	legacy, _ := secret.NewSecret(accountID, "legacy-env", secret.KindEnv, "BAD-KEY", "v", false)
	if err := repo.Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.GetByID(ctx, secret_get.SecretGetQuery{ID: legacy.ID, AuthContext: auth}); err != nil {
		t.Fatalf("read: %v", err)
	}
	same, value := legacy.Key, "rotated"
	if _, err := svc.Update(ctx, secret_update.SecretUpdateCommand{ID: legacy.ID, Key: &same, Value: &value, AuthContext: auth}); err != nil {
		t.Fatalf("an edit re-sending the stored key: %v", err)
	}
	bad := "STILL BAD"
	_, err := svc.Update(ctx, secret_update.SecretUpdateCommand{ID: legacy.ID, Key: &bad, AuthContext: auth})
	requireBadRequest(t, err, secret.ErrInvalidEnvKey)
	good := "GOOD_KEY"
	if _, err := svc.Update(ctx, secret_update.SecretUpdateCommand{ID: legacy.ID, Key: &good, AuthContext: auth}); err != nil {
		t.Fatalf("repairing the key: %v", err)
	}
	if err := svc.Delete(ctx, secret_delete.SecretDeleteCommand{ID: legacy.ID, AuthContext: auth}); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// A node attribute key is an env key too: the attribute rule is the agent's
// rule, bounded in length, and an empty key is refused.
func TestNodeAttributeKeyFollowsTheEnvKeyRule(t *testing.T) {
	for _, key := range []string{"", "1A", "A-B", "a.b", "A B"} {
		if !errors.Is(secret.ValidateAttributeKey(key), secret.ErrInvalidAttributeKey) {
			t.Errorf("attribute key %q accepted", key)
		}
		if secret.ValidEnvKey(key) {
			t.Errorf("env key %q accepted", key)
		}
	}
	for _, key := range []string{"A", "_x", "ci_agent_token", strings.Repeat("K", 64)} {
		if err := secret.ValidateAttributeKey(key); err != nil {
			t.Errorf("attribute key %q: %v", key, err)
		}
	}
	if secret.ValidateAttributeKey(strings.Repeat("K", 65)) == nil {
		t.Error("an attribute key over 64 characters accepted")
	}
}
