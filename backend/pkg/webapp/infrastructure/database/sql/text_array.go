package sql

import (
	"database/sql/driver"
	"fmt"
	"strings"
)

// textArray maps a []string onto a Postgres text[] column without pulling in
// an array-codec dependency. It only supports elements that need no quoting
// (no commas, braces, quotes or backslashes) — the secret-tag charset
// ([a-z0-9._-], enforced in the domain) satisfies that.
type textArray []string

// Value serializes to a Postgres array literal.
func (a textArray) Value() (driver.Value, error) {
	for _, el := range a {
		if strings.ContainsAny(el, `,{}"\ `) {
			return nil, fmt.Errorf("textArray element %q needs quoting, which is not supported", el)
		}
	}
	return "{" + strings.Join(a, ",") + "}", nil
}

// Scan parses a Postgres array literal (unquoted elements only).
func (a *textArray) Scan(src interface{}) error {
	var raw string
	switch v := src.(type) {
	case nil:
		*a = nil
		return nil
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return fmt.Errorf("textArray: unsupported scan source %T", src)
	}
	raw = strings.TrimPrefix(strings.TrimSuffix(raw, "}"), "{")
	if raw == "" {
		*a = textArray{}
		return nil
	}
	*a = textArray(strings.Split(raw, ","))
	return nil
}

// GormDataType tells the migrator which column type to create.
func (textArray) GormDataType() string {
	return "text[]"
}
