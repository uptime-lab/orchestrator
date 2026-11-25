package account_deletion_request

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
)

// A reason longer than the cap that ends in a multi-byte character must be stored
// as VALID UTF-8 — a raw byte slice would cut a rune in half and Postgres would
// reject it with a 500.
func TestNewTruncatesReasonOnARuneBoundary(t *testing.T) {
	acc := uuid.New()
	// "…" is 3 bytes (E2 80 A6). A run of them makes any byte-cut land mid-rune
	// for most cap values; build a reason well over the cap out of them.
	reason := strings.Repeat("…", maxReasonLen) // 3*maxReasonLen bytes
	r, err := New(acc, uuid.New(), "Acme", "Acme", reason)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Reason) > maxReasonLen {
		t.Errorf("reason not truncated: %d bytes", len(r.Reason))
	}
	if !utf8.ValidString(r.Reason) {
		t.Error("truncated reason is not valid UTF-8 — Postgres would reject it")
	}
}
