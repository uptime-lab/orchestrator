package master_token

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/master_token_create"
)

// A token with no name, and one minted already expired, are the request's
// fault. The first answered 500; the second answered 201 with a token listed
// active that every device presenting it would be refused with.
func TestCreateRefusesANamelessOrAlreadyExpiredToken(t *testing.T) {
	svc, _ := auditTokenService()
	auth := commands.NewAuthContext(uuid.New(), uuid.New())
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	for _, cmd := range []master_token_create.MasterTokenCreateCommand{
		{ID: uuid.New(), Name: "", ExpiresAt: &future, AuthContext: auth},
		{ID: uuid.New(), Name: "  ", AuthContext: auth},
		{ID: uuid.New(), Name: "fleet", ExpiresAt: &past, AuthContext: auth},
	} {
		_, err := svc.Create(context.Background(), cmd)
		var refusal *app_errors.RequestRefusalError
		if !errors.As(err, &refusal) || refusal.HTTPStatus() != http.StatusBadRequest {
			t.Errorf("Create(name=%q): err = %v, want a typed 400", cmd.Name, err)
		}
	}
	if _, err := svc.Create(context.Background(), master_token_create.MasterTokenCreateCommand{
		ID: uuid.New(), Name: "fleet", ExpiresAt: &future, AuthContext: auth,
	}); err != nil {
		t.Errorf("a named token expiring in the future: %v", err)
	}
}
