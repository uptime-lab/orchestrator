package account

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

type installKind bool

func (k installKind) IsHostedInstall(context.Context) bool { return bool(k) }

// The handler chooses the notice by the install kind; unwired it must take
// the self-hosted answer, which promises no erasure.
func TestDeletionRequestErasureExecutorFollowsTheInstallKind(t *testing.T) {
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())

	if (&DeletionRequestHandler{}).erasureExecutor(ctx) {
		t.Fatal("an unwired handler claimed an erasure executor")
	}
	hosted := &DeletionRequestHandler{}
	hosted.SetInstallKind(installKind(true))
	if !hosted.erasureExecutor(ctx) {
		t.Fatal("the hosted install lost its erasure executor")
	}
	self := &DeletionRequestHandler{}
	self.SetInstallKind(installKind(false))
	if self.erasureExecutor(ctx) {
		t.Fatal("a self-hosted install claimed an erasure executor")
	}
}
