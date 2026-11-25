package node_command

import (
	neturl "net/url"
	"regexp"
	"strings"
)

// urlInText matches an absolute http(s) URL inside free text. Deliberately
// stops at whitespace and at the punctuation that commonly closes a URL in a
// sentence or a quoted Go error ("Get \"https://…\": dial tcp …").
var urlInText = regexp.MustCompile(`https?://[^\s"'<>\x60]+`)

// RedactURLCredentialsInDetails rewrites every absolute http(s) URL in free-text
// command details down to scheme://host/path, dropping userinfo, query and
// fragment.
//
// `node_commands.details` is PLAINTEXT and is on no rewrap list, so anything
// that lands here outlives a SECRETS_ENCRYPTION_KEY rotation as
// unreadable-but-present material nobody knows to look for. Two real sources put
// credentials in it: an agent reporting a transport failure — Go's *url.Error
// embeds the whole URL, so a resolved `{{secrets.*}}` in a query string is
// copied verbatim into the report — and the platform's own capability URLs,
// whose `?exp=&sig=` HMAC is a bearer credential.
//
// The host and path are kept on purpose. This text is the only thing an operator
// has to triage a failed command with, and "download failed" with no target is
// close to useless; the query string is where the credential actually lives.
//
// That is also why the column stays PLAINTEXT rather than joining
// encryptedTargets() in rewrap.go. That list is the inventory of at-rest
// CIPHERTEXT, and its coverage check classifies anything it cannot decrypt as
// unreadable — a plaintext column added to it would raise the boot-time
// wrong-key alarm on every start of a healthy install, spending the one signal
// that catches a restore under the wrong key. The rule for this column is
// therefore "credentials must not land here", enforced at the write site, not
// "encrypt whatever lands here".
//
// Applied at the single write site (the repository), not per caller, because the
// callers that get it wrong are the ones nobody remembers to audit.
func RedactURLCredentialsInDetails(details string) string {
	if details == "" || !strings.Contains(details, "://") {
		return details
	}
	return urlInText.ReplaceAllStringFunc(details, func(raw string) string {
		// A URL at the end of a sentence usually swallows its punctuation.
		trailing := ""
		for len(raw) > 0 && strings.ContainsRune(".,;:)]}", rune(raw[len(raw)-1])) {
			trailing = string(raw[len(raw)-1]) + trailing
			raw = raw[:len(raw)-1]
		}
		parsed, err := neturl.Parse(raw)
		if err != nil {
			// Unparseable: keep the scheme and host prefix at most. Returning
			// the input would defeat the point on exactly the malformed URLs an
			// error message is most likely to carry.
			return "[redacted url]" + trailing
		}
		if parsed.RawQuery == "" && parsed.Fragment == "" && parsed.User == nil {
			return raw + trailing
		}
		// Say that a query was dropped rather than silently shortening the URL:
		// an operator comparing this against the playbook needs to know the
		// request carried parameters.
		suffix := ""
		if parsed.RawQuery != "" {
			suffix = "?[redacted]"
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String() + suffix + trailing
	})
}
