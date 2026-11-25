package image

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

func requireImageRefusal(t *testing.T, what string, err error, code string) {
	t.Helper()
	var refusal *app_errors.RequestRefusalError
	if !errors.As(err, &refusal) || refusal.HTTPStatus() != http.StatusBadRequest || refusal.ErrorCode() != code {
		t.Errorf("%s: err = %v, want a typed 400 %s", what, err, code)
	}
}

// An image needs a title, and its link is an http(s) URL or nothing: every
// other scheme (javascript:, data:, file:) is not a link an operator follows.
func TestImageCreateRefusesAnEmptyTitleAndANonHTTPURL(t *testing.T) {
	svc := NewService(newFakeImageRepo(), newFakeStorage(), &config.Config{})
	auth := commands.NewAuthContext(uuid.New(), uuid.New())
	ctx := context.Background()

	_, err := svc.Create(ctx, image_create.ImageCreateCommand{ID: uuid.New(), Title: " ", AuthContext: auth})
	requireImageRefusal(t, "empty title", err, "invalid_image_title")
	for _, u := range []string{"javascript:alert(1)", "file:///etc/passwd", "ftp://host/x", "/relative", "https://"} {
		_, err := svc.Create(ctx, image_create.ImageCreateCommand{ID: uuid.New(), Title: "t", ImageURL: u, AuthContext: auth})
		requireImageRefusal(t, u, err, "invalid_image_url")
	}
	for _, u := range []string{"", "https://cdimage.example.com/os.img.xz", "HTTP://example.com/x"} {
		if _, err := svc.Create(ctx, image_create.ImageCreateCommand{ID: uuid.New(), Title: "t", ImageURL: u, AuthContext: auth}); err != nil {
			t.Errorf("image_url %q: %v", u, err)
		}
	}
}

// An edit may not blank a title, but a record stored with an empty title
// before the rule keeps accepting edits to its other fields, and an
// unchanged link is not re-judged.
func TestImageUpdateRefusesBlankingATitleOnly(t *testing.T) {
	repo := newFakeImageRepo()
	svc := NewService(repo, newFakeStorage(), &config.Config{})
	accountID := uuid.New()
	auth := commands.NewAuthContext(accountID, uuid.New())
	ctx := context.Background()

	titled := &image.Image{ID: uuid.New(), AccountID: accountID, Title: "named"}
	untitled := &image.Image{ID: uuid.New(), AccountID: accountID, Title: "", ImageURL: "legacy-not-a-url"}
	for _, img := range []*image.Image{titled, untitled} {
		if err := repo.Create(ctx, img); err != nil {
			t.Fatal(err)
		}
	}

	_, err := svc.Update(ctx, image_update.ImageUpdateCommand{ID: titled.ID, Title: "", AuthContext: auth})
	requireImageRefusal(t, "blanking a title", err, "invalid_image_title")

	if _, err := svc.Update(ctx, image_update.ImageUpdateCommand{
		ID: untitled.ID, Title: "", Description: "edited", ImageURL: "legacy-not-a-url", AuthContext: auth,
	}); err != nil {
		t.Errorf("editing a record stored without a title: %v", err)
	}
	_, err = svc.Update(ctx, image_update.ImageUpdateCommand{ID: titled.ID, Title: "named", ImageURL: "javascript:x", AuthContext: auth})
	requireImageRefusal(t, "a changed non-http link", err, "invalid_image_url")
}
