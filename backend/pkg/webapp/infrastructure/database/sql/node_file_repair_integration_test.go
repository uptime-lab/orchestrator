package sql

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// fakeEncryptor is a reversible stand-in that produces the real at-rest SHAPE
// (the "v1:" prefix plus an unbroken base64 run), because the whole repair
// hinges on telling that shape apart from a file's text.
type fakeEncryptor struct {
	failDecrypt bool
}

func (f *fakeEncryptor) Encrypt(plaintext string) (string, error) {
	return "v1:" + base64ish(plaintext), nil
}

func (f *fakeEncryptor) Decrypt(ciphertext string) (string, error) {
	if f.failDecrypt {
		return "", errors.New("cannot decrypt with the current key")
	}
	body, ok := trimPrefix(ciphertext, "v1:")
	if !ok {
		return "", errors.New("not our envelope")
	}
	return unbase64ish(body), nil
}

// base64ish maps text to a long run of base64-legal characters. It is not
// base64 — it only has to satisfy LooksLikeStoredCiphertext and round-trip.
func base64ish(s string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	out := make([]byte, 0, len(s)*2+40)
	for i := 0; i < len(s); i++ {
		out = append(out, alphabet[s[i]>>4], alphabet[s[i]&0x0f])
	}
	for len(out) < 40 {
		out = append(out, '=')
	}
	return string(out)
}

func unbase64ish(s string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	index := func(c byte) int {
		for i := 0; i < len(alphabet); i++ {
			if alphabet[i] == c {
				return i
			}
		}
		return -1
	}
	out := make([]byte, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		hi, lo := index(s[i]), index(s[i+1])
		if hi < 0 || lo < 0 {
			break
		}
		out = append(out, byte(hi<<4|lo))
	}
	return string(out)
}

func trimPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}

// The rollback wound: an old backend wrote the operator's plain text into a row
// whose content_enc still says 1. The file must stay readable, and the row must
// be repairable.
func TestMislabeledPlaintextIsServedAndRepaired(t *testing.T) {
	repo, nodeID := versionTestRepo(t)
	repo.enc = &fakeEncryptor{}
	ctx := context.Background()

	const text = "127.0.0.1 localhost\n"
	id := uuid.New()
	err := repo.db.Exec(`INSERT INTO node_files (id, node_id, path, content, hash, content_enc, version)
		VALUES (?, ?, '/etc/hosts', ?, 'h', 1, 1)`, id, nodeID, text).Error
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}

	file, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("reading a mislabeled row: %v", err)
	}
	if file.Content != text {
		t.Fatalf("content was %q, want the plain text %q", file.Content, text)
	}

	repaired, err := repo.RepairMislabeledContent()
	if err != nil {
		t.Fatalf("repairing: %v", err)
	}
	if repaired != 1 {
		t.Fatalf("repaired %d rows, want 1", repaired)
	}

	var stored string
	if err := repo.db.Raw(`SELECT content FROM node_files WHERE id = ?`, id).Scan(&stored).Error; err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if !node_file.LooksLikeStoredCiphertext(stored) {
		t.Fatalf("the repaired row still holds %q rather than ciphertext", stored)
	}
	again, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("reading the repaired row: %v", err)
	}
	if again.Content != text {
		t.Fatalf("after repair the content reads %q, want %q", again.Content, text)
	}
}

// The other direction, which matters more: a row this backend cannot decrypt
// because of a KEY problem must keep erroring and must never be re-encrypted.
// Wrapping unreadable bytes in a second envelope destroys the original.
func TestUndecryptableCiphertextIsNeitherServedNorRepaired(t *testing.T) {
	repo, nodeID := versionTestRepo(t)
	repo.enc = &fakeEncryptor{failDecrypt: true}
	ctx := context.Background()

	ciphertext := "v1:" + base64ish("[all]\narm_64bit=1\n")
	id := uuid.New()
	err := repo.db.Exec(`INSERT INTO node_files (id, node_id, path, content, hash, content_enc, version)
		VALUES (?, ?, '/boot/firmware/config.txt', ?, 'h', 1, 1)`, id, nodeID, ciphertext).Error
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}

	if _, err := repo.GetByID(ctx, id); !errors.Is(err, node_file.ErrContentUnreadable) {
		t.Fatalf("reading gave %v, want ErrContentUnreadable", err)
	}

	repaired, err := repo.RepairMislabeledContent()
	if err != nil {
		t.Fatalf("repairing: %v", err)
	}
	if repaired != 0 {
		t.Fatalf("repaired %d rows; ciphertext must be left alone", repaired)
	}
	var stored string
	if err := repo.db.Raw(`SELECT content FROM node_files WHERE id = ?`, id).Scan(&stored).Error; err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if stored != ciphertext {
		t.Fatal("the undecryptable row was rewritten; the original ciphertext is gone")
	}
}

// It runs on every start, over a table where every row matches the predicate.
func TestRepairMislabeledContentIsIdempotentAndTerminates(t *testing.T) {
	repo, nodeID := versionTestRepo(t)
	repo.enc = &fakeEncryptor{}

	for i := 0; i < 3; i++ {
		err := repo.db.Exec(`INSERT INTO node_files (id, node_id, path, content, hash, content_enc, version)
			VALUES (?, ?, ?, 'plain text', 'h', 1, 1)`, uuid.New(), nodeID, "/etc/file"+string(rune('a'+i))).Error
		if err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}
	first, err := repo.RepairMislabeledContent()
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first != 3 {
		t.Fatalf("first run repaired %d rows, want 3", first)
	}
	second, err := repo.RepairMislabeledContent()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second != 0 {
		t.Fatalf("second run repaired %d rows, want 0", second)
	}
}
