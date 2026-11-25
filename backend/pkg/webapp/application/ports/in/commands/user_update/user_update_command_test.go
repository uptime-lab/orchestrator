package user_update

import (
	"errors"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// A supplied profile name must not be blank; omitting it is how a caller
// leaves the name alone.
func TestUserUpdateRefusesABlankName(t *testing.T) {
	for _, name := range []string{"", " \t"} {
		n := name
		if err := (UserUpdateCommand{Name: &n}).Validate(); !errors.Is(err, app_errors.ErrUserNameRequired) {
			t.Errorf("name %q: err = %v, want ErrUserNameRequired", name, err)
		}
	}
	ok := "Ada"
	if err := (UserUpdateCommand{Name: &ok}).Validate(); err != nil {
		t.Errorf("a real name: err = %v", err)
	}
	if err := (UserUpdateCommand{}).Validate(); err != nil {
		t.Errorf("an omitted name: err = %v", err)
	}
}
