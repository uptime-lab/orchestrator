package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// The secret bootstrap: `server --init-secrets DIR` writes every credential
// file a self-hosted install mounts, in the shape the secret checks accept,
// so the first boot never depends on a person typing `openssl rand` five
// times and remembering which optional files must exist empty.
//
// It lives beside the checks it satisfies on purpose: a value this function
// writes is tested against secretProblems() in this package, and a change to
// the weakness rule that the generator no longer meets fails here, not on a
// stranger's first boot.

// BootstrapSecret is one file of the set.
type BootstrapSecret struct {
	// File is the name inside the directory; the compose file mounts it at
	// /run/secrets/<File>.
	File string
	// Env is the variable the server reads the value through (<Env>_FILE
	// names the file).
	Env string
	// Generated says a random value is written; false means the file is
	// created EMPTY, which the loader reads as "not configured".
	Generated bool
	// Purpose is one line for the operator, printed after writing.
	Purpose string
}

// BootstrapSecrets is the set, in the order it is written and printed. The
// generated five have no counterpart outside this install: each has to agree
// with nothing but the containers that mount the same file. The empty ones
// are the optional credentials a fresh install has not configured — present
// so that a compose file declaring them can start, empty so that nothing is
// enabled by accident.
var BootstrapSecrets = []BootstrapSecret{
	{File: "jwt_secret", Env: "JWT_SECRET_KEY", Generated: true,
		Purpose: "signs every session token"},
	{File: "secrets_encryption_key", Env: "SECRETS_ENCRYPTION_KEY", Generated: true,
		Purpose: "encrypts stored secrets, provisioning passwords and tracked files at rest — back it up before the first boot; a database restored without it is unreadable"},
	{File: "cdn_signing_key", Env: "CDN_SIGNING_KEY", Generated: true,
		Purpose: "signs the short-lived download and EEPROM-package capabilities"},
	{File: "db_password", Env: "DB_PASSWORD", Generated: true,
		Purpose: "the PostgreSQL password; the database container reads the same file"},
	{File: "eeprom_signer_token", Env: "EEPROM_SIGNER_TOKEN", Generated: true,
		Purpose: "authenticates the server to the EEPROM signer; the signer reads the same file"},
	{File: "secrets_encryption_key_old", Env: "SECRETS_ENCRYPTION_KEY_OLD", Generated: false,
		Purpose: "empty until a key rotation; the previous key, decrypt-only"},
	{File: "invite_code", Env: "SERVER_INVITE_CODE", Generated: false,
		Purpose: "empty admits the first owner and then closes sign-up; set a code to let further organisations register with it"},
	{File: "oidc_client_secret", Env: "OIDC_CLIENT_SECRET", Generated: false,
		Purpose: "empty until single sign-on is configured"},
	{File: "s3_access_key", Env: "S3_ACCESS_KEY", Generated: false,
		Purpose: "empty until object storage is configured"},
	{File: "s3_secret_key", Env: "S3_SECRET_KEY", Generated: false,
		Purpose: "empty until object storage is configured"},
	{File: "smtp_password", Env: "EMAIL_SMTP_PASSWORD", Generated: false,
		Purpose: "empty until outbound mail needs an authenticated relay"},
	{File: "entitlement", Env: "ENTITLEMENT", Generated: false,
		Purpose: "empty means the free device cap; paste a signed entitlement here or in the UI"},
}

// bootstrapSecretBytes is the entropy per generated value: 32 bytes, printed
// as 64 hex characters — twice minProductionSecretLength, the same shape as
// `openssl rand -hex 32`, which every example in the documentation names.
const bootstrapSecretBytes = 32

// InitSecretsReport is what the one-shot prints: the directory it wrote, the
// files, and the environment lines that point the server at them.
type InitSecretsReport struct {
	Dir   string
	Files []BootstrapSecret
}

// EnvLines are the `<VAR>_FILE=<path>` assignments for the written set, in
// the written order, so an operator can paste them into an environment.
func (r InitSecretsReport) EnvLines() []string {
	lines := make([]string, 0, len(r.Files))
	for _, f := range r.Files {
		lines = append(lines, fmt.Sprintf("%s_FILE=%s", f.Env, filepath.Join(r.Dir, f.File)))
	}
	return lines
}

// ErrSecretFileExists is returned when any file of the set already exists.
// The bootstrap never overwrites: a secrets_encryption_key replaced after a
// first boot would make every encrypted row unreadable, and the refusal
// covers the WHOLE set before a byte is written, so a half-replaced directory
// cannot exist either.
var ErrSecretFileExists = errors.New("secret file already exists; the bootstrap never overwrites")

// InitSecrets creates dir (0700) if it is missing, refuses if any file of the
// set exists, then writes every file 0400: a random value for the generated
// ones, empty for the optional ones.
//
// The files are owned by whoever runs this, and 0400 means only that owner
// reads them — so it must run as the uid the containers read the files with
// (the installation page says which). The directory mode is enforced on an
// existing directory too: a secrets directory other users can traverse is
// the one thing the file mode cannot protect against.
func InitSecrets(dir string) (InitSecretsReport, error) {
	report := InitSecretsReport{Dir: dir}
	if dir == "" {
		return report, errors.New("a directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return report, fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return report, fmt.Errorf("setting %s to 0700: %w", dir, err)
	}
	// Refuse before writing anything, naming every file in the way.
	var existing []string
	for _, s := range BootstrapSecrets {
		if _, err := os.Lstat(filepath.Join(dir, s.File)); err == nil {
			existing = append(existing, s.File)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return report, fmt.Errorf("checking %s: %w", s.File, err)
		}
	}
	if len(existing) > 0 {
		sort.Strings(existing)
		return report, fmt.Errorf("%w: %v in %s", ErrSecretFileExists, existing, dir)
	}
	for _, s := range BootstrapSecrets {
		var value string
		if s.Generated {
			v, err := randomHex(bootstrapSecretBytes)
			if err != nil {
				return report, err
			}
			value = v
		}
		if err := writeSecretFile(filepath.Join(dir, s.File), value); err != nil {
			return report, err
		}
		report.Files = append(report.Files, s)
	}
	return report, nil
}

// writeSecretFile creates the file exclusively (a file that appeared between
// the check and the write is still never overwritten) at 0400, with the
// value and a trailing newline — the shape secretEnv strips.
func writeSecretFile(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrSecretFileExists, path)
		}
		return fmt.Errorf("creating %s: %w", path, err)
	}
	if value != "" {
		if _, err := f.WriteString(value + "\n"); err != nil {
			f.Close()
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	// The umask cannot widen 0400, but an unusual one could narrow it to
	// 0000; state the mode rather than trust the creation.
	return os.Chmod(path, 0o400)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}
