package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bootstrap exists to satisfy the secret checks on a first boot, so the
// test is the checks themselves: load the written files the way the server
// does (<VAR>_FILE) and ask Problems() for the secret subset.
func TestInitSecretsWritesValuesTheSecretChecksAccept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	report, err := InitSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Files) != len(BootstrapSecrets) {
		t.Fatalf("wrote %d files, want %d", len(report.Files), len(BootstrapSecrets))
	}
	for _, line := range report.EnvLines() {
		name, path, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("env line without '=': %q", line)
		}
		t.Setenv(name, path)
	}
	cfg, err := ApplyEnvironment(getDefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.Environment = EnvProduction
	if problems := cfg.secretProblems(); len(problems) != 0 {
		t.Fatalf("the generated secrets fail the checks they exist to satisfy: %v", problems)
	}
	// Each generated value is distinct: a bootstrap that derived one from
	// another would make a JWT-secret disclosure a CDN-key disclosure.
	seen := map[string]string{}
	for _, s := range BootstrapSecrets {
		raw, err := os.ReadFile(filepath.Join(dir, s.File))
		if err != nil {
			t.Fatal(err)
		}
		value := strings.TrimSpace(string(raw))
		if !s.Generated {
			if value != "" {
				t.Errorf("%s must be empty (not configured), got %d bytes", s.File, len(value))
			}
			continue
		}
		if len(value) != 2*bootstrapSecretBytes {
			t.Errorf("%s is %d characters, want %d", s.File, len(value), 2*bootstrapSecretBytes)
		}
		if other, dup := seen[value]; dup {
			t.Errorf("%s and %s carry the same value", s.File, other)
		}
		seen[value] = s.File
		if problem := weakSecretProblem(s.Env, value); problem != "" {
			t.Errorf("%s: %s", s.File, problem)
		}
	}
	// The optional files are what the loader reads as "not configured", so
	// the mailer, SSO and object storage stay off and the old key is absent.
	if len(cfg.Secrets.OldEncryptionKeys) != 0 {
		t.Errorf("an empty old-key file must read as no old key, got %v", cfg.Secrets.OldEncryptionKeys)
	}
}

func TestInitSecretsModesAreTheOwnersAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	if _, err := InitSecrets(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("directory mode = %o, want 700", got)
	}
	for _, s := range BootstrapSecrets {
		info, err := os.Stat(filepath.Join(dir, s.File))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o400 {
			t.Errorf("%s mode = %o, want 400", s.File, got)
		}
	}
}

// An existing directory is accepted — the installation page has the operator
// create it so a container bind-mount finds it owned by the right uid — but
// its mode is enforced.
func TestInitSecretsTightensAnExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := InitSecrets(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("an existing 0755 directory was left at %o, want 700", got)
	}
}

// Never overwrite, and refuse before writing anything: a replaced encryption
// key is an unreadable database, and a half-written set is a directory the
// next run cannot reason about either.
func TestInitSecretsRefusesToOverwriteAndWritesNothingThen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "secrets_encryption_key")
	if err := os.WriteFile(existing, []byte("the key every row was encrypted with\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	_, err := InitSecrets(dir)
	if !errors.Is(err, ErrSecretFileExists) {
		t.Fatalf("expected ErrSecretFileExists, got %v", err)
	}
	if !strings.Contains(err.Error(), "secrets_encryption_key") {
		t.Errorf("the refusal must name the file in the way: %v", err)
	}
	raw, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "the key every row was encrypted with\n" {
		t.Fatal("the existing key was overwritten")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("a refused run wrote %d other files; it must write none", len(entries)-1)
	}
}

// The env lines are what an operator pastes; each names the variable the
// loader reads through its _FILE form and the absolute path written.
func TestInitSecretsReportsTheFileVariables(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	report, err := InitSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	lines := report.EnvLines()
	want := map[string]bool{
		"JWT_SECRET_KEY_FILE=" + filepath.Join(dir, "jwt_secret"):                             false,
		"SECRETS_ENCRYPTION_KEY_FILE=" + filepath.Join(dir, "secrets_encryption_key"):         false,
		"CDN_SIGNING_KEY_FILE=" + filepath.Join(dir, "cdn_signing_key"):                       false,
		"DB_PASSWORD_FILE=" + filepath.Join(dir, "db_password"):                               false,
		"EEPROM_SIGNER_TOKEN_FILE=" + filepath.Join(dir, "eeprom_signer_token"):               false,
		"SERVER_INVITE_CODE_FILE=" + filepath.Join(dir, "invite_code"):                        false,
		"SECRETS_ENCRYPTION_KEY_OLD_FILE=" + filepath.Join(dir, "secrets_encryption_key_old"): false,
	}
	for _, l := range lines {
		if _, ok := want[l]; ok {
			want[l] = true
		}
	}
	for l, seen := range want {
		if !seen {
			t.Errorf("missing env line %q in %v", l, lines)
		}
	}
}
