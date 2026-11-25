package secret

import (
	"reflect"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret_tag"
)

// The API description lists the tag colors from the request structs' enums
// tags. They must name exactly the palette the service validates against, or
// the documented values and the accepted ones drift apart.
func TestTagColorEnumsMatchThePalette(t *testing.T) {
	want := strings.Join(secret_tag.Palette, ",")
	for _, typ := range []reflect.Type{
		reflect.TypeOf(upsertTagRequest{}),
		reflect.TypeOf(updateTagRequest{}),
	} {
		field, ok := typ.FieldByName("Color")
		if !ok {
			t.Fatalf("%s has no Color field", typ.Name())
		}
		if got := field.Tag.Get("enums"); got != want {
			t.Errorf("%s.Color enums = %q, want %q", typ.Name(), got, want)
		}
	}
}
