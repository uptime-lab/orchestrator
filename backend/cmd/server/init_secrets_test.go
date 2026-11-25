package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A plain start must not be mistaken for the one-shot, and both spellings of
// the flag must be.
func TestInitSecretsFlagIsRecognisedInBothSpellings(t *testing.T) {
	if _, present := initSecretsDir(nil); present {
		t.Fatal("no arguments must mean a plain server start")
	}
	if _, present := initSecretsDir([]string{"--config", "x"}); present {
		t.Fatal("an unrelated argument must mean a plain server start")
	}
	if dir, present := initSecretsDir([]string{"--init-secrets", "/run/x"}); !present || dir != "/run/x" {
		t.Fatalf("--init-secrets DIR: present=%v dir=%q", present, dir)
	}
	if dir, present := initSecretsDir([]string{"--init-secrets=/run/y"}); !present || dir != "/run/y" {
		t.Fatalf("--init-secrets=DIR: present=%v dir=%q", present, dir)
	}
	if dir, present := initSecretsDir([]string{"--init-secrets"}); !present || dir != "" {
		t.Fatalf("a bare flag must ask for usage: present=%v dir=%q", present, dir)
	}
}

// The command prints what it wrote and the lines to paste, and exits 0; run
// twice it refuses with exit 1 and leaves the first set alone.
func TestInitSecretsCommandWritesOnceThenRefuses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	var out, errOut bytes.Buffer
	handled, code := initSecretsCommand([]string{"--init-secrets", dir}, &out, &errOut)
	if !handled || code != 0 {
		t.Fatalf("handled=%v code=%d stderr=%s", handled, code, errOut.String())
	}
	for _, want := range []string{"jwt_secret", "SECRETS_ENCRYPTION_KEY_FILE=" + filepath.Join(dir, "secrets_encryption_key"), "Back up secrets_encryption_key"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, out.String())
		}
	}
	first, err := os.ReadFile(filepath.Join(dir, "jwt_secret"))
	if err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errOut.Reset()
	handled, code = initSecretsCommand([]string{"--init-secrets=" + dir}, &out, &errOut)
	if !handled || code != 1 {
		t.Fatalf("second run: handled=%v code=%d", handled, code)
	}
	if !strings.Contains(errOut.String(), "never overwrites") {
		t.Errorf("the refusal must say why: %s", errOut.String())
	}
	second, err := os.ReadFile(filepath.Join(dir, "jwt_secret"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("the second run replaced a secret")
	}

	handled, code = initSecretsCommand([]string{"--init-secrets"}, &out, &errOut)
	if !handled || code != 2 {
		t.Fatalf("bare flag: handled=%v code=%d, want usage exit 2", handled, code)
	}
}
