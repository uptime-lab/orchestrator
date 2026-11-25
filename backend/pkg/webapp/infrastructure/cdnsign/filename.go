package cdnsign

import (
	"path"
	"strings"
	"unicode"
)

// ParamFileName is the query parameter carrying the name a /cdn response should
// be saved under. It becomes a Content-Disposition filename.
//
// It is deliberately NOT covered by the signature. The HMAC binds the storage
// path — the thing that decides which bytes are served — and every existing
// signed URL (install_os, the agent package, the EEPROM per-serial package) was
// minted under that scheme; widening the signed material would invalidate them
// all for a parameter that changes only a label. What makes an unsigned label
// safe is SanitizeFileName, which every consumer must apply, not the signature.
const ParamFileName = "filename"

// maxFileNameLen bounds the label. Long enough for any real image name, short
// enough that the header cannot be used as a payload carrier.
const maxFileNameLen = 128

// SanitizeFileName reduces an arbitrary string to something safe to place in a
// Content-Disposition header, or returns "" if nothing usable survives.
//
// This is the security boundary for ParamFileName, so it is a strict allowlist
// rather than an escape: anything that is not an unreserved ASCII filename
// character is dropped. In particular it removes CR, LF and the double quote —
// the header-injection vectors — and path separators, so the label can never
// suggest a directory. Callers get "" for input that is empty after filtering
// and must supply their own fallback; returning a placeholder here would hide
// the fact that the caller's own name was unusable.
func SanitizeFileName(name string) string {
	// A caller may hand us a path; only its last element can be a file name.
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if name == "." || name == "/" {
		return ""
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case r > unicode.MaxASCII:
			// Non-ASCII is dropped rather than transliterated. RFC 6266 has a
			// filename* form for it, but a second encoding is a second thing to
			// get wrong in a header, and image names here are machine-generated
			// or operator-typed ASCII in practice.
			continue
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_' || r == '+':
			b.WriteRune(r)
		case r == ' ':
			// A space is legal in a quoted Content-Disposition value, but it is
			// also the thing that makes shell round trips lose the name. An
			// underscore keeps the word boundary and survives everything.
			b.WriteRune('_')
		default:
			continue
		}
	}
	out := b.String()

	// A leading dot would produce a hidden file; leading dashes are read as
	// flags by most command-line tools the downloaded file will meet next.
	out = strings.TrimLeft(out, ".-")
	if len(out) > maxFileNameLen {
		out = out[:maxFileNameLen]
	}
	return out
}
