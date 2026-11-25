// Package secret_tag adds presentation (a color) and a management lifecycle on
// top of the bare tag strings that secrets carry. Membership stays the string
// on secrets.tags / secret_attachments.tag — a secret_tag row is purely a color
// override plus the anchor the Tags tab manages. A tag without a row still
// renders with a deterministic default color, so inline tagging never breaks.
package secret_tag

import (
	"errors"
	"hash/fnv"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/tags"
	"github.com/google/uuid"
)

var (
	// ErrNameRequired is returned when a tag name is empty.
	ErrNameRequired = errors.New("tag name is required")
	// ErrInvalidColor rejects colors outside the design-system palette.
	ErrInvalidColor = errors.New("invalid tag color")
	// ErrNameTaken is returned when renaming onto an existing tag.
	ErrNameTaken = errors.New("a tag with this name already exists")
	// ErrInvalidName mirrors the shared tag charset rule.
	ErrInvalidName = tags.ErrInvalidTag
)

// Palette is the fixed set of tag colors, mirroring the frontend BadgeColor
// palette (shared/ui/badge). Kept in sync deliberately: the API validates the
// color so a client cannot store an off-brand value.
var Palette = []string{
	"red", "pink", "purple", "violet", "blue", "lightBlue",
	"green", "lime", "yellow", "orange",
}

// ValidColor reports whether a color is in the palette.
func ValidColor(color string) bool {
	for _, c := range Palette {
		if c == color {
			return true
		}
	}
	return false
}

// DefaultColorFor deterministically maps a tag name to a palette color, so a
// tag that has no explicit row still renders with a stable, varied color.
func DefaultColorFor(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return Palette[h.Sum32()%uint32(len(Palette))]
}

// Tag is a color override + management anchor for a secret tag string.
type Tag struct {
	AccountID uuid.UUID
	Name      string
	Color     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewTag validates and normalizes a tag name and color. An empty color falls
// back to the deterministic default for the name.
func NewTag(accountID uuid.UUID, name, color string) (*Tag, error) {
	normalized, err := tags.Normalize([]string{name})
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return nil, ErrNameRequired
	}
	name = normalized[0]
	if color == "" {
		color = DefaultColorFor(name)
	}
	if !ValidColor(color) {
		return nil, ErrInvalidColor
	}
	now := time.Now()
	return &Tag{AccountID: accountID, Name: name, Color: color, CreatedAt: now, UpdatedAt: now}, nil
}

// NormalizeName validates and normalizes a bare tag name (for rename/delete).
func NormalizeName(name string) (string, error) {
	normalized, err := tags.Normalize([]string{name})
	if err != nil {
		return "", err
	}
	if len(normalized) == 0 {
		return "", ErrNameRequired
	}
	return normalized[0], nil
}
