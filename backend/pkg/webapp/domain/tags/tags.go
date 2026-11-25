// Package tags holds the shared tag rules used by secrets, SSH keys and
// master tokens: free-form purpose labels, normalized and charset-restricted
// (the restriction also keeps them safe to embed in Postgres array literals).
package tags

import (
	"errors"
	"strings"
)

var (
	// ErrTooManyTags caps the tag list per entity.
	ErrTooManyTags = errors.New("an entry can carry at most 10 tags")
	// ErrInvalidTag rejects tags outside the allowed charset/length.
	ErrInvalidTag = errors.New("tags must be 1-40 characters of a-z, 0-9, '.', '_' or '-'")
)

const maxTags = 10

func validTagChar(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-'
}

// Normalize lowercases, trims, de-duplicates (order-preserving) and validates
// a tag list.
func Normalize(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, item := range raw {
		tag := strings.ToLower(strings.TrimSpace(item))
		if tag == "" {
			continue
		}
		if len(tag) > 40 {
			return nil, ErrInvalidTag
		}
		for _, c := range tag {
			if !validTagChar(c) {
				return nil, ErrInvalidTag
			}
		}
		if seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	if len(out) > maxTags {
		return nil, ErrTooManyTags
	}
	return out, nil
}

// Merge unions two tag lists, preserving order and the cap.
func Merge(existing, extra []string) ([]string, error) {
	return Normalize(append(append([]string{}, existing...), extra...))
}
