package account_deletion_request

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The typed name is the CONTROL, not the dialog. These are the inputs a real
// person produces, and refusing them would push someone into copy-pasting,
// which defeats the confirmation entirely.
func TestNameMatchesToleratesCaseAndSurroundingSpace(t *testing.T) {
	for _, typed := range []string{"Acme Ltd", "acme ltd", "  Acme Ltd  ", "ACME LTD"} {
		if !NameMatches("Acme Ltd", typed) {
			t.Errorf("NameMatches(%q) = false, want true", typed)
		}
	}
}

func TestNameMatchesRejectsAnythingElse(t *testing.T) {
	for _, typed := range []string{"", "   ", "Acme", "Acme Ltd.", "Acme  Ltd"} {
		if NameMatches("Acme Ltd", typed) {
			t.Errorf("NameMatches(%q) = true, want false", typed)
		}
	}
}

// The one input a caller supplies by accident is an empty string, and an
// account with no name must not be confirmable by it.
func TestAnEmptyAccountNameIsNeverConfirmed(t *testing.T) {
	if NameMatches("", "") {
		t.Fatal("an empty typed name confirmed an unnamed account")
	}
}

func TestNewRefusesAMismatch(t *testing.T) {
	_, err := New(uuid.New(), uuid.New(), "Acme Ltd", "Acme", "")
	if !errors.Is(err, ErrNameMismatch) {
		t.Fatalf("err = %v, want ErrNameMismatch", err)
	}
}

func TestNewBoundsTheReason(t *testing.T) {
	long := strings.Repeat("x", maxReasonLen*2)
	req, err := New(uuid.New(), uuid.New(), "Acme", "acme", long)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(req.Reason) != maxReasonLen {
		t.Errorf("reason length = %d, want it bounded at %d", len(req.Reason), maxReasonLen)
	}
}

func TestNewCarriesTheActor(t *testing.T) {
	account, user := uuid.New(), uuid.New()
	req, err := New(account, user, "Acme", "  ACME ", "  moving in-house  ")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if req.AccountID != account || req.RequestedBy != user {
		t.Error("the request does not carry who asked, for which account")
	}
	if req.Reason != "moving in-house" {
		t.Errorf("reason = %q, want it trimmed", req.Reason)
	}
	if req.RequestedAt.IsZero() {
		t.Error("requested_at is zero")
	}
}
