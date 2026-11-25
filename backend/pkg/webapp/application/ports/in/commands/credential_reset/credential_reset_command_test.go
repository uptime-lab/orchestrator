package credential_reset

import (
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// A chosen password bcrypt cannot hash is refused by validation, before the
// account is touched; an empty one still means "generate".
func TestResetPasswordCommandRefusesAPasswordBcryptCannotHash(t *testing.T) {
	cmd := ResetPasswordCommand{Email: "owner@example.com", Password: strings.Repeat("A", user.MaxPasswordLength+1)}
	if err := cmd.Validate(); err == nil {
		t.Fatal("a password past the ceiling passed validation")
	}
	cmd.Password = strings.Repeat("A", user.MaxPasswordLength)
	if err := cmd.Validate(); err != nil {
		t.Fatalf("a password of exactly the ceiling must pass, got %v", err)
	}
	cmd.Password = ""
	if err := cmd.Validate(); err != nil {
		t.Fatalf("an empty password (generate one) must pass, got %v", err)
	}
}
