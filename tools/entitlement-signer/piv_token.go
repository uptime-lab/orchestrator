//go:build piv

package main

// The PIV transport for the token-held production key, the same library and
// connection shape as tools/agent-signer: enumerate the PC/SC readers, open
// each, pick the YubiKey by serial, read the slot's public key, and let the
// token authenticate the PIN and demand the touch. The private key never
// leaves the token. Built only with `-tags piv`: piv-go needs cgo and the
// PC/SC library (a system framework on macOS, libpcsclite-dev on Linux).

import (
	"crypto"
	"errors"
	"fmt"
	"os"

	"github.com/go-piv/piv-go/v2/piv"
	"golang.org/x/term"
)

func init() {
	openTokenKey = openPIVKey
}

type pivKey struct {
	crypto.Signer
	yk *piv.YubiKey
}

func (k *pivKey) Close() error { return k.yk.Close() }

func openPIVKey(cfg pivConfig) (tokenKey, error) {
	slot, ok := piv.RetiredKeyManagementSlot(cfg.slot)
	if !ok {
		return nil, fmt.Errorf("PIV slot %x is not a retired key-management slot", cfg.slot)
	}
	yk, err := openYubiKey(cfg.serial)
	if err != nil {
		return nil, err
	}
	key, err := keyInSlot(yk, slot, cfg)
	if err != nil {
		yk.Close()
		return nil, err
	}
	return key, nil
}

// openYubiKey opens the YubiKey with the given serial, or — with serial 0 —
// the only one connected. Two tokens and no --serial is a refusal, never a
// guess: the wrong token is a wrong key.
func openYubiKey(serial uint) (*piv.YubiKey, error) {
	cards, err := piv.Cards()
	if err != nil {
		return nil, fmt.Errorf("listing smart-card readers: %w", err)
	}
	var found []*piv.YubiKey
	for _, c := range cards {
		yk, err := piv.Open(c)
		if err != nil {
			continue
		}
		s, err := yk.Serial()
		if err != nil {
			yk.Close()
			continue
		}
		if serial != 0 {
			if uint(s) == serial {
				return yk, nil
			}
			yk.Close()
			continue
		}
		found = append(found, yk)
	}
	if serial != 0 {
		return nil, fmt.Errorf("no YubiKey with serial %d found (plugged in?)", serial)
	}
	switch len(found) {
	case 0:
		return nil, errors.New("no YubiKey found (plugged in?)")
	case 1:
		return found[0], nil
	default:
		for _, yk := range found {
			yk.Close()
		}
		return nil, fmt.Errorf("%d YubiKeys are connected; name one with --serial", len(found))
	}
}

func keyInSlot(yk *piv.YubiKey, slot piv.Slot, cfg pivConfig) (tokenKey, error) {
	auth := piv.KeyAuth{PINPrompt: promptPIN}
	if cfg.pinEnv != "" {
		auth.PINPrompt = pinFromEnv(cfg.pinEnv)
	}
	// KeyInfo (YubiKey firmware 5.3 and later) reads the slot's public key
	// and its policies without a certificate. Without it, the certificate
	// ykman wrote beside the key is the fallback, and the touch policy is
	// unknown, so --touch refuses.
	var pub crypto.PublicKey
	ki, err := yk.KeyInfo(slot)
	if err == nil {
		pub = ki.PublicKey
		auth.PINPolicy = ki.PINPolicy
		if cfg.touch && ki.TouchPolicy != piv.TouchPolicyAlways {
			return nil, fmt.Errorf("PIV slot %x does not require a touch for every signature (--touch); "+
				"the entitlement key is generated with --touch-policy ALWAYS (docs/ENTITLEMENT.md)", cfg.slot)
		}
		if ki.Origin == piv.OriginImported {
			fmt.Fprintf(os.Stderr, "WARNING: the key in PIV slot %x was IMPORTED, not generated on the token; a copy may exist elsewhere\n", cfg.slot)
		}
	} else {
		if cfg.touch {
			return nil, fmt.Errorf("PIV slot %x: cannot read the touch policy (%v); --touch needs YubiKey firmware 5.3 or later", cfg.slot, err)
		}
		cert, cerr := yk.Certificate(slot)
		if cerr != nil {
			return nil, fmt.Errorf("PIV slot %x: no key metadata (%v) and no certificate (%v); is the key generated?", cfg.slot, err, cerr)
		}
		pub = cert.PublicKey
	}
	if err := checkTokenPublicKey(cfg.slot, pub); err != nil {
		return nil, err
	}
	priv, err := yk.PrivateKey(slot, pub, auth)
	if err != nil {
		return nil, fmt.Errorf("PIV slot %x: %w", cfg.slot, err)
	}
	signer, ok := priv.(crypto.Signer)
	if !ok {
		return nil, errors.New("PIV private key is not a crypto.Signer")
	}
	return &pivKey{Signer: signer, yk: yk}, nil
}

// promptPIN reads the PIN from the terminal without echo, as agent-signer
// does. It runs only when the token needs a PIN, so `pubkey` never asks.
func promptPIN() (string, error) {
	fmt.Fprint(os.Stderr, "PIV PIN: ")
	pin, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading the PIN: %w", err)
	}
	if len(pin) == 0 {
		return "", errors.New("empty PIN")
	}
	return string(pin), nil
}
