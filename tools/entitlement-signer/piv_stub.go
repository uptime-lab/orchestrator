//go:build !piv

package main

// The default build carries no PIV transport: it stays standard library
// only and cgo-free, so `go build ./...`, the CI jobs and the deploy
// preflight's `go run . verify` need no smart-card library. --piv-slot says
// how to get the build that has one.
func init() {
	openTokenKey = func(pivConfig) (tokenKey, error) { return nil, errNoPIVSupport }
}
