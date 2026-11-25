// Package signing exposes the trusted agent-signing public keys (multi-trust) so
// the server can embed them into the install.sh it hands out. The install script
// verifies the downloaded agent package's detached signature (ECDSA P-384 /
// SHA-384) against these keys with openssl before installing it as root.
//
// Private keys live only on YubiKeys (PIV slot 9c) — nothing here is secret.
package signing

import (
	"embed"
	"sort"
	"strings"
)

//go:embed keys/*.pem
var keyFS embed.FS

// TrustedPublicKeysPEM returns every trusted public key concatenated as PEM text,
// in deterministic (filename) order. Empty string only if the embed set is missing
// (a build mistake) — callers that inject this into a fail-closed verifier then
// simply have no key to match, so nothing verifies.
func TrustedPublicKeysPEM() string {
	entries, err := keyFS.ReadDir("keys")
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var b strings.Builder
	for _, n := range names {
		data, err := keyFS.ReadFile("keys/" + n)
		if err != nil {
			continue
		}
		b.Write(data)
		if !strings.HasSuffix(string(data), "\n") {
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
