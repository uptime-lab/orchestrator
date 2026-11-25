package main

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// The token-held production key, seen through an interface so that
// everything but the PC/SC transport is built and tested without a token
// and without cgo. The transport is piv_token.go (build tag `piv`, through
// github.com/go-piv/piv-go as tools/agent-signer uses it); piv_stub.go is the
// default build's refusal.

// pivConfig says which key on which token to use and how to authenticate.
type pivConfig struct {
	// slot is a RETIRED key-management slot, 0x82..0x95. Never 9c: that slot
	// holds the agent-signing key (tools/agent-signer), a different key with
	// a different purpose that this tool must not be able to reach.
	slot uint32
	// serial picks the YubiKey; 0 means "the only one connected".
	serial uint
	// pinEnv names an environment variable holding the PIN; empty prompts
	// on the terminal.
	pinEnv string
	// touch refuses a slot whose touch policy is not ALWAYS, so a key
	// generated without a touch requirement cannot quietly sign files.
	touch bool
}

// tokenKey is a key on a token: its public half without a PIN, and a
// signer that authenticates when it is first asked to sign. Close releases
// the card.
type tokenKey interface {
	crypto.Signer
	Close() error
}

// openTokenKey opens the key named by cfg. Set by piv_token.go or
// piv_stub.go; tests replace it with a fake.
var openTokenKey func(cfg pivConfig) (tokenKey, error)

// errNoPIVSupport is the default build's answer to --piv-slot.
var errNoPIVSupport = errors.New("this entitlement-signer was built without PIV support; build it with `go build -tags piv` " +
	"(cgo; the PC/SC framework on macOS, libpcsclite-dev on Linux) to use --piv-slot")

// parsePIVSlot reads a slot as two hex digits and admits only the retired
// key-management slots. The standard slots (9a, 9c, 9d, 9e) are refused by
// name: 9c holds the agent-signing key.
func parsePIVSlot(s string) (uint32, error) {
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x"), 16, 8)
	if err != nil {
		return 0, fmt.Errorf("--piv-slot %q is not a hex slot number such as 82", s)
	}
	if v == 0x9c {
		return 0, errors.New("--piv-slot 9c holds the AGENT-signing key; the entitlement key lives in a retired slot (82..95), docs/ENTITLEMENT.md")
	}
	if v < 0x82 || v > 0x95 {
		return 0, fmt.Errorf("--piv-slot %x is not a retired key-management slot (82..95), where the entitlement key lives", v)
	}
	return uint32(v), nil
}

// pinFromEnv returns a PIN source for cfg.pinEnv: the variable's value, or
// a refusal when it is unset or empty — an empty PIN sent to the token would
// spend one of its few retries for nothing.
func pinFromEnv(name string) func() (string, error) {
	return func() (string, error) {
		pin := os.Getenv(name)
		if pin == "" {
			return "", fmt.Errorf("--pin-env %s: the variable is unset or empty", name)
		}
		return pin, nil
	}
}

// checkTokenPublicKey refuses a token key the server's trust store could not
// hold. On YubiKey 5 firmware before 5.7, PIV offers no Ed25519, so in
// practice this is "P-384, not P-256 or RSA".
func checkTokenPublicKey(slot uint32, pub crypto.PublicKey) error {
	if _, err := acceptedKey(pub); err != nil {
		return fmt.Errorf("PIV slot %x: %w; generate the entitlement key with -a ECCP384 (docs/ENTITLEMENT.md)", slot, err)
	}
	return nil
}

// pubkeyCmd exports a slot's public key as the PKIX "PUBLIC KEY" PEM the
// backend embeds in backend/pkg/webapp/domain/entitlement/keys/. Reading a
// public key needs no PIN and no touch.
func pubkeyCmd(args []string) error {
	fs := flag.NewFlagSet("pubkey", flag.ExitOnError)
	slotArg := fs.String("piv-slot", "", "retired PIV slot holding the key, hex 82..95")
	var cfg pivConfig
	fs.UintVar(&cfg.serial, "serial", 0, "YubiKey serial (default: the only YubiKey connected)")
	out := fs.String("out", "", "output PEM path; refuses to overwrite")
	_ = fs.Parse(args)
	if *slotArg == "" || *out == "" {
		usage()
	}
	slot, err := parsePIVSlot(*slotArg)
	if err != nil {
		return err
	}
	cfg.slot = slot
	key, err := openTokenKey(cfg)
	if err != nil {
		return err
	}
	defer key.Close()
	return writePublicKeyPEM(*out, key.Public(), slot)
}

func writePublicKeyPEM(path string, pub crypto.PublicKey, slot uint32) error {
	if err := checkTokenPublicKey(slot, pub); err != nil {
		return err
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return err
	}
	if err := writeNew(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s: the public key of PIV slot %x (SHA-256 of its PKIX DER %s)\n", path, slot, keyFingerprint(pub))
	return nil
}
