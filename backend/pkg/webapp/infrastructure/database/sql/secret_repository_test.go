package sql

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

// failingEncryptor stands in for a key that can no longer open what is stored.
type failingEncryptor struct{ Encryptor }

func (failingEncryptor) Decrypt(string) (string, error) {
	return "", errors.New("cipher: message authentication failed")
}

// The mapper is what decides whether one bad row costs the whole read: it used
// to return an error, which List/GetByID/GetByIDs all propagated.
func TestToDomainDecryptedSliceKeepsEveryRowAndFlagsTheBadOne(t *testing.T) {
	repo := &SecretRepository{encryptor: failingEncryptor{}}
	models := []SecretModel{
		{ID: uuid.New(), AccountID: uuid.New(), Name: "unset"},
		{ID: uuid.New(), AccountID: uuid.New(), Name: "orphan", ValueEncrypted: "v1:whatever"},
	}

	out := repo.toDomainDecryptedSlice(models)
	if len(out) != 2 {
		t.Fatalf("got %d rows, want 2 — one undecryptable row dropped the others", len(out))
	}
	if out[0].ValueUnreadable {
		t.Error("a row with no stored value is reported unreadable")
	}
	if !out[1].ValueUnreadable || out[1].Value != "" {
		t.Errorf("undecryptable row: unreadable=%t value=%q", out[1].ValueUnreadable, out[1].Value)
	}
}
