package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/credential_reset"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/credential_reset"
)

// A plain start, and the init-secrets one-shot, must not be read as a reset.
func TestResetArgsAbsentMeansAPlainStart(t *testing.T) {
	for _, args := range [][]string{nil, {"--config", "x"}, {"--init-secrets", "/run/x"}, {"--email", "a@example.com"}} {
		if _, present, _ := parseResetArgs(args); present {
			t.Errorf("%q must not be a reset", args)
		}
	}
}

func TestResetArgsAccepted(t *testing.T) {
	cases := []struct {
		args []string
		want resetRequest
	}{
		{[]string{"--reset-password", "--email", "a@example.com"}, resetRequest{mode: resetPassword, email: "a@example.com"}},
		{[]string{"--email=a@example.com", "--reset-password", "--password-stdin"}, resetRequest{mode: resetPassword, email: "a@example.com", passwordStdin: true}},
		{[]string{"--reset-totp", "--email", " a@example.com "}, resetRequest{mode: resetTOTP, email: "a@example.com"}},
		{[]string{"--reset-totp", "--help"}, resetRequest{mode: resetTOTP, help: true}},
	}
	for _, c := range cases {
		got, present, err := parseResetArgs(c.args)
		if !present || err != nil || got != c.want {
			t.Errorf("%q: present=%v err=%v got=%+v want=%+v", c.args, present, err, got, c.want)
		}
	}
}

// Every malformed form is a usage error (exit 2), and in particular a
// password can never ride the command line, where `ps` shows it.
func TestResetArgsRefused(t *testing.T) {
	for _, args := range [][]string{
		{"--reset-password"},
		{"--reset-password", "--email"},
		{"--reset-password", "--email", "--password-stdin"},
		{"--reset-password", "--email="},
		{"--reset-password", "--reset-totp", "--email", "a@example.com"},
		{"--reset-password", "--email", "a@example.com", "--email", "b@example.com"},
		{"--reset-totp", "--email", "a@example.com", "--password-stdin"},
		{"--reset-password", "--email", "a@example.com", "--password", "hunter22"},
		{"--reset-password", "--email", "a@example.com", "--password=hunter22"},
		{"--reset-password", "--email", "a@example.com", "stray"},
	} {
		_, present, err := parseResetArgs(args)
		if !present || !errors.Is(err, errResetUsage) {
			t.Errorf("%q: present=%v err=%v, want a usage error", args, present, err)
			continue
		}
		var out, errOut bytes.Buffer
		handled, code := resetCredentialsCommand(args, strings.NewReader(""), &out, &errOut)
		if !handled || code != 2 {
			t.Errorf("%q: handled=%v code=%d, want usage exit 2", args, handled, code)
		}
		if out.Len() != 0 {
			t.Errorf("%q: a usage error wrote to stdout: %q", args, out.String())
		}
		if strings.Contains(errOut.String(), "hunter22") {
			t.Errorf("%q: the refusal echoed the password", args)
		}
	}
}

// --password-stdin with nothing on stdin is refused before any database is
// reached (exit 2, not a connection failure).
func TestResetPasswordStdinEmptyIsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	handled, code := resetCredentialsCommand([]string{"--reset-password", "--email", "a@example.com", "--password-stdin"}, strings.NewReader("\n"), &out, &errOut)
	if !handled || code != 2 {
		t.Fatalf("handled=%v code=%d stderr=%s", handled, code, errOut.String())
	}
}

func TestReadStdinPasswordTakesTheFirstLineVerbatim(t *testing.T) {
	got, err := readStdinPassword(strings.NewReader(" pass word \r\nsecond line\n"))
	if err != nil || got != " pass word " {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = readStdinPassword(strings.NewReader("no-newline"))
	if err != nil || got != "no-newline" {
		t.Fatalf("got %q, %v", got, err)
	}
}

type fakeResetter struct {
	res        *results.CredentialResetResult
	err        error
	gotPass    cmd.ResetPasswordCommand
	gotTOTP    cmd.ResetTOTPCommand
	calledPass bool
	calledTOTP bool
}

func (f *fakeResetter) ResetPassword(_ context.Context, c cmd.ResetPasswordCommand) (*results.CredentialResetResult, error) {
	f.calledPass, f.gotPass = true, c
	return f.res, f.err
}

func (f *fakeResetter) ResetTOTP(_ context.Context, c cmd.ResetTOTPCommand) (*results.CredentialResetResult, error) {
	f.calledTOTP, f.gotTOTP = true, c
	return f.res, f.err
}

// Standard output carries the temporary password and nothing else, so
// `PW=$(… --reset-password …)` captures exactly it.
func TestRunResetPrintsOnlyThePasswordOnStdout(t *testing.T) {
	f := &fakeResetter{res: &results.CredentialResetResult{UserID: uuid.New(), TemporaryPassword: "Tmp-Pass-123456789", SessionsRevoked: true, AuditedAccounts: 1}}
	var out, errOut bytes.Buffer
	code := runReset(context.Background(), resetRequest{mode: resetPassword, email: "a@example.com"}, "", f, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if out.String() != "Tmp-Pass-123456789\n" {
		t.Fatalf("stdout = %q", out.String())
	}
	if strings.Contains(errOut.String(), "Tmp-Pass-123456789") {
		t.Fatal("the password leaked onto stderr")
	}
	// Standard output is often redirected to a file, so a message on standard
	// error must not point "above" at a password the terminal never shows.
	if strings.Contains(errOut.String(), "above") || !strings.Contains(errOut.String(), "standard output") {
		t.Fatalf("stderr must say the password is on standard output: %q", errOut.String())
	}
	if f.gotPass.Email != "a@example.com" || f.gotPass.Password != "" {
		t.Fatalf("command = %+v", f.gotPass)
	}
}

// A reset no audit trail holds still exits 0, but says so in a sentence
// instead of "recorded in … 0 account(s)".
func TestRunResetSaysWhenNoAuditTrailHoldsTheReset(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  results.CredentialResetResult
		want string
	}{
		{"no membership", results.CredentialResetResult{SessionsRevoked: true}, noAuditTrailNoMembership},
		{"memberships unreadable", results.CredentialResetResult{SessionsRevoked: true, MembershipsUnreadable: true}, noAuditTrailUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.res
			res.UserID = uuid.New()
			var out, errOut bytes.Buffer
			code := runReset(context.Background(), resetRequest{mode: resetTOTP, email: "a@example.com"}, "", &fakeResetter{res: &res}, &out, &errOut)
			if code != 0 {
				t.Fatalf("code=%d stderr=%s", code, errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.want) {
				t.Fatalf("stderr %q lacks %q", errOut.String(), tc.want)
			}
			if strings.Contains(errOut.String(), "0 account(s)") {
				t.Fatalf("stderr still claims a recording: %q", errOut.String())
			}
		})
	}
	var out, errOut bytes.Buffer
	res := results.CredentialResetResult{UserID: uuid.New(), SessionsRevoked: true, AuditedAccounts: 2}
	_ = runReset(context.Background(), resetRequest{mode: resetTOTP, email: "a@example.com"}, "", &fakeResetter{res: &res}, &out, &errOut)
	if strings.Contains(errOut.String(), "WARNING") || !strings.Contains(errOut.String(), "2 account(s)") {
		t.Fatalf("a recorded reset reads %q", errOut.String())
	}
}

// A chosen password is passed through and never printed.
func TestRunResetWithStdinPasswordPrintsNothing(t *testing.T) {
	f := &fakeResetter{res: &results.CredentialResetResult{UserID: uuid.New(), SessionsRevoked: true}}
	var out, errOut bytes.Buffer
	code := runReset(context.Background(), resetRequest{mode: resetPassword, email: "a@example.com", passwordStdin: true}, "chosen-password", f, &out, &errOut)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("code=%d stdout=%q", code, out.String())
	}
	if f.gotPass.Password != "chosen-password" {
		t.Fatalf("the chosen password did not reach the service: %+v", f.gotPass)
	}
	if strings.Contains(errOut.String(), "chosen-password") {
		t.Fatal("the chosen password was echoed")
	}
}

// Every refusal is exit 1, with nothing on stdout.
func TestRunResetRefusals(t *testing.T) {
	for _, err := range []error{
		app_errors.ErrUserNotFound,
		credential_reset.ErrAmbiguousEmail,
		credential_reset.ErrNoEnabledAuthenticator,
		errors.New("connection refused"),
	} {
		for _, mode := range []resetMode{resetPassword, resetTOTP} {
			f := &fakeResetter{err: err}
			var out, errOut bytes.Buffer
			code := runReset(context.Background(), resetRequest{mode: mode, email: "a@example.com"}, "", f, &out, &errOut)
			if code != 1 || out.Len() != 0 || errOut.Len() == 0 {
				t.Errorf("%v/%d: code=%d stdout=%q stderr=%q", err, mode, code, out.String(), errOut.String())
			}
		}
	}
}

// A credential that changed while the revocation failed still prints the
// password — it is the user's only one now — and exits 1.
func TestRunResetFailedRevocationStillPrintsThePassword(t *testing.T) {
	f := &fakeResetter{res: &results.CredentialResetResult{UserID: uuid.New(), TemporaryPassword: "Tmp-Pass-123456789", SessionsRevoked: false}}
	var out, errOut bytes.Buffer
	code := runReset(context.Background(), resetRequest{mode: resetPassword, email: "a@example.com"}, "", f, &out, &errOut)
	if code != 1 || out.String() != "Tmp-Pass-123456789\n" {
		t.Fatalf("code=%d stdout=%q", code, out.String())
	}
	if !strings.Contains(errOut.String(), "revoking") {
		t.Fatalf("stderr must say what failed: %s", errOut.String())
	}
}

func TestRunResetTOTPRoutesToTheTOTPReset(t *testing.T) {
	f := &fakeResetter{res: &results.CredentialResetResult{UserID: uuid.New(), SessionsRevoked: true}}
	var out, errOut bytes.Buffer
	if code := runReset(context.Background(), resetRequest{mode: resetTOTP, email: "a@example.com"}, "", f, &out, &errOut); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !f.calledTOTP || f.calledPass || f.gotTOTP.Email != "a@example.com" || out.Len() != 0 {
		t.Fatalf("calledTOTP=%v calledPass=%v stdout=%q", f.calledTOTP, f.calledPass, out.String())
	}
}
