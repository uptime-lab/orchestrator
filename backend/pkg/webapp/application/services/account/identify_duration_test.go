package account

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_update"
)

// An identify duration outside its range is the request's fault; the bound
// was already worded for the caller and reached them as a 500.
func TestAnOutOfRangeIdentifyDurationIsARequestRefusal(t *testing.T) {
	accountID := uuid.New()
	svc, _ := auditAccountService(accountID)
	for _, v := range []int{-5, 9, 3601} {
		d := v
		_, err := svc.UpdateAccount(context.Background(), account_update.AccountUpdateCommand{
			ID: accountID, IdentifyDurationSec: &d, AuthContext: commands.NewAuthContext(accountID, uuid.New()),
		})
		var refusal *app_errors.RequestRefusalError
		if !errors.As(err, &refusal) || refusal.HTTPStatus() != http.StatusBadRequest {
			t.Errorf("identify_duration_sec=%d: err = %v, want a typed 400", v, err)
		}
	}
}
