package account_update

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      uuid.UUID
		input   string
		wantErr bool
		want    string // expected Name after a successful validate
	}{
		{name: "typical name", id: uuid.New(), input: "Acme Robotics", want: "Acme Robotics"},
		{name: "trims surrounding whitespace", id: uuid.New(), input: "  Acme  ", want: "Acme"},
		{name: "two characters is the minimum", id: uuid.New(), input: "Ok", want: "Ok"},
		{name: "one character is too short", id: uuid.New(), input: "A", wantErr: true},
		{name: "whitespace only is too short", id: uuid.New(), input: "     ", wantErr: true},
		{name: "empty is too short", id: uuid.New(), input: "", wantErr: true},
		{name: "whitespace padding cannot smuggle a short name", id: uuid.New(), input: "  A  ", wantErr: true},
		{name: "100 characters is allowed", id: uuid.New(), input: strings.Repeat("a", 100), want: strings.Repeat("a", 100)},
		{name: "101 characters is too long", id: uuid.New(), input: strings.Repeat("a", 101), wantErr: true},
		// Bounds are counted in runes: 100 Cyrillic characters are 200 bytes.
		{name: "100 non-Latin characters is allowed", id: uuid.New(), input: strings.Repeat("я", 100), want: strings.Repeat("я", 100)},
		{name: "101 non-Latin characters is too long", id: uuid.New(), input: strings.Repeat("я", 101), wantErr: true},
		{name: "missing account id", id: uuid.Nil, input: "Acme", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := AccountUpdateCommand{ID: tc.id, Name: &tc.input}

			err := cmd.Validate()

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if cmd.Name == nil || *cmd.Name != tc.want {
				t.Errorf("Name = %v, want %q", cmd.Name, tc.want)
			}
		})
	}
}
