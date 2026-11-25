module entitlement-signer

go 1.26.0

// Pinned to match backend/ and agent/, for the reason agent-signer
// gives: a module built against an older stdlib is the one govulncheck
// reports on alone.
toolchain go1.26.11

// piv-go, x/term and the x/crypto floor match tools/agent-signer exactly
// (same library, same token): bump the two modules together. They are
// compiled only by the `-tags piv` build (piv_token.go), which needs cgo and
// the PC/SC library; the default build is standard library only.
require (
	github.com/go-piv/piv-go/v2 v2.6.0
	golang.org/x/term v0.48.0
)

require (
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
)
