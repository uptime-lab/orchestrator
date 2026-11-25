// entitlement-signer issues and checks the signed entitlement files the
// platform's device cap reads (backend/pkg/webapp/domain/entitlement).
//
// Two kinds of signing key. The PRODUCTION key is ECDSA P-384 in a retired
// PIV slot of a YubiKey (generated there with ykman, never exported; the
// token's firmware has no Ed25519): `sign --piv-slot` and `pubkey --piv-slot`
// reach it through github.com/go-piv/piv-go, which needs cgo and the PC/SC
// library, so that code is behind the `piv` build tag (piv_token.go). A
// DEVELOPMENT key is a file — Ed25519 from `keygen`, or an ECDSA P-384
// PKCS#8/SEC1 PEM — and the default build, standard library only and with no
// cgo, signs with it and verifies anything. The deploy preflight runs the
// default build.
//
//	go build -tags piv -o entitlement-signer .       # the token-capable build
//	entitlement-signer pubkey --piv-slot 82 --out entitlement-signing-1.pem
//	entitlement-signer sign   --piv-slot 82 --touch --customer acme \
//	                          --device-cap 100 --expires 2027-01-01 --kind subscription \
//	                          [--serial N] [--pin-env NAME] \
//	                          [--grace-days 30] [--note "..."] [--issued-at 2026-01-01]
//	entitlement-signer keygen --out dev-signing           # development key only
//	entitlement-signer sign   --key dev-signing.key ...    # same flags as above
//	entitlement-signer verify --pub entitlement-signing-1.pem [--pub ...] [--in FILE] \
//	                          [--require-active] [--min-cap N]
//	entitlement-signer verify --select --pub KEY.pem [--pub ...] --in BOOT --in ROW ... \
//	                          [--require-active] [--min-cap N]
//
// Every --pub file must parse on its own, as the server requires of its
// embedded key files. --select judges the file the SERVER puts in force among
// several offered ones (the boot file first, then the applied files newest
// issued first): the newest non-lapsed by signed issue date, as
// services/entitlement resolves it.
//
// The file format is documented in docs/ENTITLEMENT.md. This tool and the
// backend package implement it independently and must agree byte for byte;
// the backend's tests verify a fixture produced here to keep them honest.
package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	formPrefix    = "entl1"
	signingPrefix = "orchestrator-entitlement/1\n"
	version       = 1

	defaultGraceDays = 30
	maxGraceDays     = 3650
	// maxTerm bounds how far out an expiry may be set. Five years is longer
	// than any contract on offer; a typo in the year must be caught here, not
	// discovered when a file turns out to be perpetual.
	maxTerm = 5 * 365 * 24 * time.Hour
	// maxIssueSkew is how far ahead an issue date may lie (the server's
	// entitlement.MaxIssueSkew).
	maxIssueSkew = 24 * time.Hour
	// freeDeviceCap is the server's entitlement.FreeDeviceCap: what an install
	// admits with no file at all. It is a FLOOR — the server enforces a
	// positive signed cap below it as this value — so the signer never issues
	// such a cap. A copy, pinned to the server's source by
	// TestFreeDeviceCapMatchesTheServer (the signer does not import the
	// backend module).
	freeDeviceCap = 25
	// maxFormLength is the server's entitlement.MaxFormLength: a paste longer
	// than this is refused before any signature check, so a file that only
	// verifies here would still be rejected there. Kept equal by
	// TestBoundsMatchTheServerSource, which reads the backend's source.
	maxFormLength = 8 * 1024
	// The signature shapes the server's Parse admits before any key is
	// tried: an Ed25519 signature's exact size, or an ASN.1 DER ECDSA
	// SEQUENCE no longer than a P-384 signature can be.
	maxECDSASignatureSize = 104
	minECDSASignatureSize = 8
)

// capBelowFreeFloorError refuses a positive --device-cap below the free cap.
// Such a file would grant less than no file at all; the server would accept
// it and enforce the free cap anyway, so issuing it is always a mistake.
type capBelowFreeFloorError struct {
	Cap int
}

func (e *capBelowFreeFloorError) Error() string {
	return fmt.Sprintf("--device-cap %d is below the free cap of %d devices, which every install admits with no file; "+
		"issue %d or more, or 0 (0 means unlimited)", e.Cap, freeDeviceCap, freeDeviceCap)
}

// belowFreeFloor reports a positive cap under the free cap.
func belowFreeFloor(c int) bool { return c > 0 && c < freeDeviceCap }

// enforcedCap is the cap the server enforces for a signed cap while the file
// is active or in grace: the signed value, never below the free cap; 0 stays
// unlimited.
func enforcedCap(c int) int {
	if belowFreeFloor(c) {
		return freeDeviceCap
	}
	return c
}

var b64 = base64.RawURLEncoding

// document mirrors entitlement.Document, in the same field order: the order
// IS the canonical encoding.
type document struct {
	Version    int       `json:"version"`
	CustomerID string    `json:"customer_id"`
	DeviceCap  int       `json:"device_cap"`
	IssuedAt   time.Time `json:"issued_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Kind       string    `json:"kind"`
	GraceDays  int       `json:"grace_days"`
	Note       string    `json:"note"`
}

var kinds = []string{"evaluation", "subscription", "hosted"}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygenCmd(os.Args[2:])
	case "sign":
		err = signCmd(os.Args[2:], os.Stdout)
	case "verify":
		err = verifyCmd(os.Args[2:], os.Stdin, os.Stdout, time.Now())
	case "pubkey":
		err = pubkeyCmd(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  entitlement-signer keygen --out NAME                      a DEVELOPMENT Ed25519 pair: NAME.key (private) and NAME.pem (public)")
	fmt.Fprintln(os.Stderr, "                            the PRODUCTION key is generated ON the YubiKey with ykman, never here (docs/ENTITLEMENT.md)")
	fmt.Fprintln(os.Stderr, "  entitlement-signer pubkey --piv-slot HEX [--serial N] --out FILE.pem")
	fmt.Fprintln(os.Stderr, "                            exports the slot's public key as the PKIX PEM the backend embeds (build with -tags piv)")
	fmt.Fprintln(os.Stderr, "  entitlement-signer sign   (--key FILE | --piv-slot HEX [--serial N] [--pin-env NAME] [--touch])")
	fmt.Fprintln(os.Stderr, "                            --customer ID --device-cap N --expires YYYY-MM-DD --kind KIND [--grace-days N] [--note TEXT] [--issued-at YYYY-MM-DD]")
	fmt.Fprintln(os.Stderr, "  entitlement-signer verify [--select] --pub FILE [--pub FILE...] [--in FILE...] [--require-active] [--min-cap N]")
	fmt.Fprintln(os.Stderr, "                            reads the paste form from --in or stdin; the two flags make a preflight fail by exit code;")
	fmt.Fprintln(os.Stderr, "                            --select judges the file the server puts in force among several --in, given in the server's order")
	os.Exit(2)
}

func keygenCmd(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "output name; writes NAME.key and NAME.pem")
	_ = fs.Parse(args)
	if *out == "" {
		usage()
	}
	// A DEVELOPMENT key. The production key never exists as a file: it is
	// generated inside the YubiKey (docs/ENTITLEMENT.md, "The production
	// signing key") and its public half exported with `pubkey --piv-slot`.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return err
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	if err := writeNew(*out+".key", privPEM, 0o600); err != nil {
		return err
	}
	if err := writeNew(*out+".pem", pubPEM, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s.key (private, keep offline) and %s.pem (public)\n", *out, *out)
	return nil
}

// writeNew refuses to overwrite: a key file replaced by accident is a key
// nobody can recover, and a public key replaced by accident silently changes
// what the next build trusts.
func writeNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

type signOptions struct {
	keyPath   string
	piv       pivConfig
	pivSlot   string
	customer  string
	deviceCap int
	expires   string
	kind      string
	graceDays int
	note      string
	issuedAt  string
	now       time.Time
}

func signCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	o := signOptions{now: time.Now()}
	fs.StringVar(&o.keyPath, "key", "", "development private key PEM (PKCS#8 Ed25519, or PKCS#8/SEC1 ECDSA P-384)")
	fs.StringVar(&o.pivSlot, "piv-slot", "", "sign with the key in this retired PIV slot, hex 82..95 (the production key; needs the -tags piv build)")
	fs.UintVar(&o.piv.serial, "serial", 0, "with --piv-slot: the YubiKey serial (default: the only YubiKey connected)")
	fs.StringVar(&o.piv.pinEnv, "pin-env", "", "with --piv-slot: read the PIV PIN from this environment variable instead of prompting")
	fs.BoolVar(&o.piv.touch, "touch", false, "with --piv-slot: refuse unless the slot's touch policy is ALWAYS")
	fs.StringVar(&o.customer, "customer", "", "customer id the file is bound to")
	fs.IntVar(&o.deviceCap, "device-cap", -1, "devices the install may admit; 0 = unlimited")
	fs.StringVar(&o.expires, "expires", "", "expiry date, YYYY-MM-DD (end of that day, UTC) or RFC 3339")
	fs.StringVar(&o.kind, "kind", "", "evaluation | subscription | hosted")
	fs.IntVar(&o.graceDays, "grace-days", defaultGraceDays, "days after expiry during which the cap still holds")
	fs.StringVar(&o.note, "note", "", "short note shown on the subscription card")
	fs.StringVar(&o.issuedAt, "issued-at", "", "issue date (default: now); YYYY-MM-DD or RFC 3339")
	_ = fs.Parse(args)
	form, err := sign(o)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, form)
	return nil
}

func sign(o signOptions) (string, error) {
	if (o.keyPath == "") == (o.pivSlot == "") {
		return "", errors.New("exactly one of --key (a development key file) and --piv-slot (the token-held key) is required")
	}
	if o.customer == "" || o.deviceCap < 0 || o.expires == "" || o.kind == "" {
		return "", errors.New("--customer, --device-cap, --expires and --kind are required")
	}
	// Refused here, at issue, and NOT in validate: verify shares validate, and
	// a sub-floor file signed elsewhere is one the server accepts (as the free
	// cap), so verify reports it as a warning rather than calling it invalid.
	if belowFreeFloor(o.deviceCap) {
		return "", &capBelowFreeFloorError{Cap: o.deviceCap}
	}
	issued := o.now.UTC().Truncate(time.Second)
	if o.issuedAt != "" {
		var err error
		issued, err = parseDate(o.issuedAt, false)
		if err != nil {
			return "", fmt.Errorf("--issued-at: %w", err)
		}
	}
	expires, err := parseDate(o.expires, true)
	if err != nil {
		return "", fmt.Errorf("--expires: %w", err)
	}
	// The two refusals that exist because a signing mistake is otherwise
	// discovered by a customer: a file that is dead on arrival, and a file
	// that outlives any contract on offer.
	if !expires.After(o.now) {
		return "", fmt.Errorf("--expires %s is in the past", expires.Format(time.RFC3339))
	}
	if expires.After(o.now.Add(maxTerm)) {
		return "", fmt.Errorf("--expires %s is more than five years out", expires.Format(time.RFC3339))
	}
	// The newest valid file wins by its issue date, so a date far ahead would
	// outrank every file issued after it; the server refuses the same.
	if issued.After(o.now.Add(maxIssueSkew)) {
		return "", fmt.Errorf("--issued-at %s is more than 24 hours in the future", issued.Format(time.RFC3339))
	}
	doc := document{
		Version:    version,
		CustomerID: o.customer,
		DeviceCap:  o.deviceCap,
		IssuedAt:   issued,
		ExpiresAt:  expires,
		Kind:       o.kind,
		GraceDays:  o.graceDays,
		Note:       o.note,
	}
	if err := doc.validate(); err != nil {
		return "", err
	}
	payload, err := encode(doc)
	if err != nil {
		return "", err
	}
	// The key is opened LAST, after every refusal above: a token asks for a
	// PIN and a touch, which a typo in --expires must not cost.
	var priv crypto.Signer
	if o.pivSlot != "" {
		slot, err := parsePIVSlot(o.pivSlot)
		if err != nil {
			return "", err
		}
		cfg := o.piv
		cfg.slot = slot
		key, err := openTokenKey(cfg)
		if err != nil {
			return "", err
		}
		defer key.Close()
		priv = key
		fmt.Fprintln(os.Stderr, "touch the YubiKey if it is blinking…")
	} else {
		priv, err = loadPrivateKey(o.keyPath)
		if err != nil {
			return "", err
		}
	}
	sig, err := signMessage(priv, signingInput(payload))
	if err != nil {
		return "", err
	}
	form := formPrefix + "." + b64.EncodeToString(payload) + "." + b64.EncodeToString(sig)
	// Verify what was produced against the key's own public half, as the
	// server will: a token answering with a signature in some other encoding
	// is caught here, not by the customer's install.
	if _, err := verify(form, []crypto.PublicKey{priv.Public()}); err != nil {
		return "", fmt.Errorf("the signature just made does not verify against its own key: %w", err)
	}
	return form, nil
}

// acceptedKey admits only the two key types the server's trust store loads
// (entitlement.acceptedKey): Ed25519, and ECDSA on P-384.
func acceptedKey(parsed any) (crypto.PublicKey, error) {
	switch k := parsed.(type) {
	case ed25519.PublicKey:
		return k, nil
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P384() {
			return nil, fmt.Errorf("ECDSA public key is on %s, want P-384", k.Curve.Params().Name)
		}
		return k, nil
	default:
		return nil, fmt.Errorf("public key is %T, want Ed25519 or ECDSA P-384", parsed)
	}
}

// signMessage signs the prefixed message with the algorithm the key's public
// half selects: Ed25519 over the message, or ECDSA P-384 over its SHA-384
// with an ASN.1 DER signature (what a PIV token and ecdsa.SignASN1 return,
// and what tools/agent-signer produces for the agent artifacts).
func signMessage(priv crypto.Signer, msg []byte) ([]byte, error) {
	pub, err := acceptedKey(priv.Public())
	if err != nil {
		return nil, err
	}
	if _, ok := pub.(ed25519.PublicKey); ok {
		return priv.Sign(rand.Reader, msg, crypto.Hash(0))
	}
	digest := sha512.Sum384(msg)
	return priv.Sign(rand.Reader, digest[:], crypto.SHA384)
}

// verifyOne checks one signature against one key with that key's algorithm.
func verifyOne(key crypto.PublicKey, msg, sig []byte) bool {
	switch k := key.(type) {
	case ed25519.PublicKey:
		return len(sig) == ed25519.SignatureSize && ed25519.Verify(k, msg, sig)
	case *ecdsa.PublicKey:
		digest := sha512.Sum384(msg)
		return ecdsa.VerifyASN1(k, digest[:], sig)
	default:
		return false
	}
}

func plausibleSignature(sig []byte) bool {
	if len(sig) == ed25519.SignatureSize {
		return true
	}
	return len(sig) >= minECDSASignatureSize && len(sig) <= maxECDSASignatureSize && sig[0] == 0x30
}

// sameKey compares two public keys of any stdlib type.
func sameKey(a, b crypto.PublicKey) bool {
	k, ok := a.(interface{ Equal(crypto.PublicKey) bool })
	return ok && k.Equal(b)
}

// keyFingerprint is the SHA-256 of a key's PKIX DER, printed by pubkey so
// the operator can compare the exported file with the token's key.
func keyFingerprint(k crypto.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func (d document) validate() error {
	if strings.TrimSpace(d.CustomerID) == "" {
		return errors.New("--customer must not be blank")
	}
	if len([]rune(d.CustomerID)) > 128 {
		return errors.New("--customer is longer than 128 characters")
	}
	if d.DeviceCap < 0 {
		return errors.New("--device-cap must be 0 (unlimited) or positive")
	}
	if d.IssuedAt.IsZero() || d.ExpiresAt.IsZero() {
		return errors.New("issued_at and expires_at are required")
	}
	known := false
	for _, k := range kinds {
		if d.Kind == k {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("--kind %q is not one of %s", d.Kind, strings.Join(kinds, ", "))
	}
	if d.GraceDays < 0 || d.GraceDays > maxGraceDays {
		return fmt.Errorf("--grace-days must be between 0 and %d", maxGraceDays)
	}
	if len([]rune(d.Note)) > 512 {
		return errors.New("--note is longer than 512 characters")
	}
	if !d.ExpiresAt.After(d.IssuedAt) {
		return errors.New("--expires must be after --issued-at")
	}
	return nil
}

// parseDate accepts a bare date or an RFC 3339 instant. A bare date names a
// whole day: as an expiry it means the END of that day (the customer is
// covered through the date on the contract), as an issue date its start.
func parseDate(s string, endOfDay bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Truncate(time.Second), nil
	}
	day, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is neither YYYY-MM-DD nor RFC 3339", s)
	}
	if endOfDay {
		return day.Add(24*time.Hour - time.Second).UTC(), nil
	}
	return day.UTC(), nil
}

func encode(doc document) ([]byte, error) {
	doc.IssuedAt = doc.IssuedAt.UTC().Truncate(time.Second)
	doc.ExpiresAt = doc.ExpiresAt.UTC().Truncate(time.Second)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func signingInput(payload []byte) []byte {
	in := make([]byte, 0, len(signingPrefix)+len(payload))
	in = append(in, signingPrefix...)
	return append(in, payload...)
}

// fileList is a repeatable path flag (--pub, --in).
type fileList []string

func (f *fileList) String() string     { return strings.Join(*f, ",") }
func (f *fileList) Set(v string) error { *f = append(*f, v); return nil }

// loadKeyFiles parses each key file ON ITS OWN, as the server's loadTrusted
// does with its embedded keys/*.pem: one file that does not parse (an empty
// file, a key that is neither Ed25519 nor ECDSA P-384, a stray PEM block)
// makes the server trust NO key at all, so every entitlement is rejected and
// the install falls to the free cap. Concatenating the files first would let such a file vanish into its
// neighbours, and the preflight would pass a build that rejects everything.
func loadKeyFiles(paths []string) ([]crypto.PublicKey, error) {
	var keys []crypto.PublicKey
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("key file %s: %w", filepath.Base(p), err)
		}
		k, err := parsePublicKeys(data)
		if err != nil {
			return nil, fmt.Errorf("key file %s: %w; the server trusts no key when one of its key files fails to parse, so every file would be rejected", filepath.Base(p), err)
		}
		keys = append(keys, k...)
	}
	if len(keys) == 0 {
		return nil, errors.New("no key file given")
	}
	return keys, nil
}

// candidate is one offered file that verified, in the order it was offered.
type candidate struct {
	name string
	doc  document
}

// selectInForce is the server's rule (services/entitlement resolveUncached),
// kept equal to it: among the candidates in the order they were offered —
// the boot file first, then the applied files newest issued first — the
// newest by SIGNED issue date that has not lapsed; if every one has lapsed,
// the newest of those. A tie keeps the earlier one. Nil when there is none.
func selectInForce(cands []candidate, now time.Time) *candidate {
	var best *candidate
	for i := range cands {
		c := &cands[i]
		if best == nil {
			best = c
			continue
		}
		bestLapsed := lapsedAt(best.doc, now)
		cLapsed := lapsedAt(c.doc, now)
		switch {
		case bestLapsed && !cLapsed:
			best = c
		case !bestLapsed && cLapsed:
			// keep best
		case c.doc.IssuedAt.After(best.doc.IssuedAt):
			best = c
		}
	}
	return best
}

func lapsedAt(d document, now time.Time) bool {
	return !now.Before(d.ExpiresAt.Add(time.Duration(d.GraceDays) * 24 * time.Hour))
}

// effectiveCapAt is the cap the server enforces for the file in force
// (entitlement.Document.EffectiveCap): its enforced cap while active or in
// grace, the free cap once lapsed.
func effectiveCapAt(d document, now time.Time) int {
	if lapsedAt(d, now) {
		return freeDeviceCap
	}
	return enforcedCap(d.DeviceCap)
}

// accept is what makes an offered file a candidate on the server: within the
// paste bound, a valid signature over terms the server accepts, and an issue
// date not more than a day ahead.
func accept(form string, keys []crypto.PublicKey, now time.Time) (document, error) {
	if len(form) > maxFormLength {
		return document{}, fmt.Errorf("the file is %d bytes, over the server's bound of %d", len(form), maxFormLength)
	}
	doc, err := verify(form, keys)
	if err != nil {
		return document{}, err
	}
	if doc.IssuedAt.After(now.Add(maxIssueSkew)) {
		return document{}, fmt.Errorf("issued_at %s is more than 24 hours in the future; the server refuses it", doc.IssuedAt.Format(time.RFC3339))
	}
	return doc, nil
}

func verifyCmd(args []string, stdin io.Reader, out io.Writer, now time.Time) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	var pubPaths, inPaths fileList
	fs.Var(&pubPaths, "pub", "public key PEM; repeat for every key file the build embeds (each must parse)")
	fs.Var(&inPaths, "in", "file holding the paste form (default: stdin); repeat with --select")
	selectMode := fs.Bool("select", false, "judge the file the SERVER puts in force among every --in, in the server's order (boot file first, then applied files newest issued first)")
	requireActive := fs.Bool("require-active", false, "fail unless the file (with --select: the file in force) is active now (before its expiry)")
	minCap := fs.Int("min-cap", 0, "fail unless the enforced device_cap is 0 (unlimited) or at least this many devices")
	_ = fs.Parse(args)
	if len(pubPaths) == 0 {
		usage()
	}
	keys, err := loadKeyFiles(pubPaths)
	if err != nil {
		return err
	}
	if *selectMode {
		return verifySelect(inPaths, keys, out, now, *requireActive, *minCap)
	}
	if len(inPaths) > 1 {
		return errors.New("several --in files need --select: the server puts ONE of them in force, and that is the one to judge")
	}
	var formBytes []byte
	if len(inPaths) == 1 {
		formBytes, err = os.ReadFile(inPaths[0])
	} else {
		formBytes, err = io.ReadAll(io.LimitReader(stdin, 64*1024))
	}
	if err != nil {
		return err
	}
	doc, err := accept(string(formBytes), keys, now)
	if err != nil {
		return err
	}
	report(out, doc, now)
	return assertPreflight(doc, now, *requireActive, *minCap)
}

// verifySelect reads every offered file, keeps those the server would accept
// (an EMPTY input is an absent file, as an unset boot file is on the server;
// a rejected one is reported and is not a candidate, as on the server),
// selects the one in force by the server's rule, and holds THAT file to the
// preflight assertions. No candidate at all fails: the server would run on
// the free cap.
func verifySelect(inPaths []string, keys []crypto.PublicKey, out io.Writer, now time.Time, requireActive bool, minCap int) error {
	if len(inPaths) == 0 {
		return errors.New("--select needs at least one --in")
	}
	var cands []candidate
	for _, p := range inPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		name := filepath.Base(p)
		if strings.TrimSpace(string(data)) == "" {
			fmt.Fprintf(out, "%s: empty, not a candidate\n", name)
			continue
		}
		doc, err := accept(string(data), keys, now)
		if err != nil {
			fmt.Fprintf(out, "%s: REJECTED by the server's rules: %v\n", name, err)
			continue
		}
		fmt.Fprintf(out, "%s: verifies (issued_at %s, %s)\n", name, doc.IssuedAt.Format(time.RFC3339), phase(doc, now))
		cands = append(cands, candidate{name: name, doc: doc})
	}
	best := selectInForce(cands, now)
	if best == nil {
		return fmt.Errorf("no offered file verifies; the server runs on the free cap of %d devices", freeDeviceCap)
	}
	fmt.Fprintf(out, "IN FORCE (the server's selection): %s\n", best.name)
	report(out, best.doc, now)
	return assertPreflight(best.doc, now, requireActive, minCap)
}

func report(out io.Writer, doc document, now time.Time) {
	fmt.Fprintf(out, "OK: signature valid\n")
	fmt.Fprintf(out, "customer_id: %s\nkind: %s\ndevice_cap: %s\nissued_at: %s\nexpires_at: %s\ngrace_days: %d\nnote: %s\nstate now: %s\n",
		doc.CustomerID, doc.Kind, capString(doc.DeviceCap),
		doc.IssuedAt.Format(time.RFC3339), doc.ExpiresAt.Format(time.RFC3339),
		doc.GraceDays, doc.Note, phase(doc, now))
	if belowFreeFloor(doc.DeviceCap) {
		fmt.Fprintf(out, "WARNING: device_cap %d is below the free cap of %d; the server enforces %d while the file is in force\n",
			doc.DeviceCap, freeDeviceCap, freeDeviceCap)
	}
}

// assertPreflight makes the two assertions a deploy preflight makes, as an
// exit code rather than a line someone has to read. The cap is the one the
// server ENFORCES for this file now (never below the free cap; the free cap
// once lapsed), so the preflight and the server cannot disagree.
func assertPreflight(doc document, now time.Time, requireActive bool, minCap int) error {
	if requireActive && !now.Before(doc.ExpiresAt) {
		return fmt.Errorf("the file is not active now (%s)", phase(doc, now))
	}
	if enforced := effectiveCapAt(doc, now); minCap > 0 && enforced != 0 && enforced < minCap {
		return fmt.Errorf("device_cap %d does not cover the %d admitted devices the install holds", enforced, minCap)
	}
	return nil
}

func capString(n int) string {
	if n == 0 {
		return "unlimited"
	}
	return fmt.Sprint(n)
}

func phase(d document, now time.Time) string {
	graceUntil := d.ExpiresAt.Add(time.Duration(d.GraceDays) * 24 * time.Hour)
	switch {
	case now.Before(d.ExpiresAt):
		return "active"
	case now.Before(graceUntil):
		return "grace (until " + graceUntil.Format(time.RFC3339) + ")"
	default:
		return "lapsed (new admissions fall to the free cap)"
	}
}

func verify(form string, keys []crypto.PublicKey) (document, error) {
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		}
		return r
	}, form)
	parts := strings.Split(compact, ".")
	if len(parts) != 3 || parts[0] != formPrefix {
		return document{}, errors.New("not an entitlement file")
	}
	payload, err := b64.DecodeString(parts[1])
	if err != nil {
		return document{}, errors.New("not an entitlement file")
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || !plausibleSignature(sig) {
		return document{}, errors.New("not an entitlement file")
	}
	input := signingInput(payload)
	ok := false
	for _, k := range keys {
		if verifyOne(k, input, sig) {
			ok = true
			break
		}
	}
	if !ok {
		return document{}, errors.New("BAD: signature invalid")
	}
	var doc document
	if err := json.Unmarshal(payload, &doc); err != nil {
		return document{}, fmt.Errorf("payload: %w", err)
	}
	if doc.Version != version {
		return document{}, fmt.Errorf("unsupported version %d", doc.Version)
	}
	// The terms too, as the server checks them after the signature: a file
	// produced by anything but `sign` can carry a valid signature over terms
	// the server refuses, and the preflight must not call that file OK.
	if err := doc.validate(); err != nil {
		return document{}, fmt.Errorf("BAD: the server would refuse this document: %w", err)
	}
	return doc, nil
}

// loadPrivateKey reads a DEVELOPMENT signing key from a file: PKCS#8
// Ed25519 (what keygen writes), or ECDSA P-384 as PKCS#8 or SEC1 ("EC
// PRIVATE KEY", what `openssl ecparam -genkey` writes). A key the server's
// trust store could not hold is refused here rather than at verification.
func loadPrivateKey(path string) (crypto.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM block", path)
	}
	var parsed any
	switch block.Type {
	case "PRIVATE KEY":
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		parsed, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("%s: PEM block %q is not a PKCS#8 PRIVATE KEY or an EC PRIVATE KEY", path, block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	priv, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("%s: key is %T, not a signing key", path, parsed)
	}
	if _, err := acceptedKey(priv.Public()); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return priv, nil
}

func parsePublicKeys(data []byte) ([]crypto.PublicKey, error) {
	var keys []crypto.PublicKey
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("unexpected PEM block %q", block.Type)
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key, err := acceptedKey(parsed)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, errors.New("no public key in PEM data")
	}
	return keys, nil
}
