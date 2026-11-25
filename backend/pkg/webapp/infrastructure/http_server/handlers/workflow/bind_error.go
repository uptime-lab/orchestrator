package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// bindErrorMessage words a request-body decode failure for the caller. The
// decoder's own text names the Go types it was filling ("expected=
// workflow_template_import.PortableDocument"), which is the server's internal
// vocabulary, not the API's: the caller is told the JSON field and the JSON
// kind it must be, or the byte offset of a syntax error.
func bindErrorMessage(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		if typeErr.Field != "" {
			return fmt.Sprintf("invalid request: %s must be %s, not %s", typeErr.Field, jsonKind(typeErr.Type), typeErr.Value)
		}
		return fmt.Sprintf("invalid request: the body must be %s, not %s", jsonKind(typeErr.Type), typeErr.Value)
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Sprintf("invalid request: malformed JSON at byte %d", syntaxErr.Offset)
	}
	return "invalid request"
}

// jsonKind names the JSON value a Go type decodes from.
func jsonKind(t reflect.Type) string {
	if t == nil {
		return "a different JSON value"
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		return "an object"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "a number"
	default:
		return "a different JSON value"
	}
}
