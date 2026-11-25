package results

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Applying a saved version takes the version's id; the save answer carries it,
// so a client does not have to list the versions to find it.
func TestFileUpdateResultCarriesTheVersionID(t *testing.T) {
	id := uuid.New()
	b, err := json.Marshal(NewFileUpdateResult(id, "/etc/hosts", 2, "saved", "ok"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"id":"`+id.String()+`"`) {
		t.Fatalf("the save answer lacks the version id: %s", b)
	}
}
