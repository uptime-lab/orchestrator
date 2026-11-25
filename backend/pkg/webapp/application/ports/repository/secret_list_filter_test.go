package repository

import "testing"

// The sort allowlist lives on the filter type so that every consumer —
// including ones written after the SQL adapter — inherits the injection guard
// by construction rather than by luck.
func TestSecretListFilterSortColumnAllowlist(t *testing.T) {
	cases := []struct {
		sort string
		want string
	}{
		{"name", "name"},
		{"created_at", "created_at"},
		{"updated_at", "updated_at"},
		{"", "name"},
		{"NAME", "name"},                       // exact match only
		{"name; DROP TABLE secrets--", "name"}, // injection falls back
		{"created_at, (SELECT 1)", "name"},     // injection falls back
		{"last_used_at", "name"},               // real column, not allowed
	}
	for _, tc := range cases {
		f := SecretListFilter{Sort: tc.sort}
		if got := f.SortColumn(); got != tc.want {
			t.Errorf("SortColumn(%q) = %q, want %q", tc.sort, got, tc.want)
		}
	}
}

func TestSecretListFilterOrderDirection(t *testing.T) {
	cases := []struct {
		order string
		want  string
	}{
		{"desc", "DESC"},
		{"asc", "ASC"},
		{"", "ASC"},
		{"DESC", "ASC"},             // exact match only, like the old adapter
		{"desc; DROP TABLE", "ASC"}, // anything else is ASC
	}
	for _, tc := range cases {
		f := SecretListFilter{Order: tc.order}
		if got := f.OrderDirection(); got != tc.want {
			t.Errorf("OrderDirection(%q) = %q, want %q", tc.order, got, tc.want)
		}
	}
}
