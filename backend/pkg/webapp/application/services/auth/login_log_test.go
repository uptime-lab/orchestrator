package auth

import (
	"bytes"
	"log/slog"
	"regexp"
	"strings"
	"testing"
)

// signInLog runs one refused password sign-in and returns what it logged,
// with the timestamps removed so two runs can be compared line for line.
func signInLog(t *testing.T, accountExists bool, typed string) string {
	t.Helper()
	f := newLoginTimingFixture(t, typed, "correct horse battery")
	if !accountExists {
		f.users.byEmail = nil
	}
	var buf bytes.Buffer
	f.svc.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := f.svc.Login(typed, "the wrong password", ""); err == nil {
		t.Fatal("a wrong password signed in")
	}
	return regexp.MustCompile(`time=\S+ `).ReplaceAllString(buf.String(), "")
}

// The log must not answer "which addresses have accounts here": a refused
// sign-in writes the same lines whether the address is unknown or the
// password is wrong, never the address as typed, and not at ERROR — a
// refused credential is the API working.
func TestARefusedSignInLogsTheSameForAnUnknownAddressAndAWrongPassword(t *testing.T) {
	const typed = "Someone.Typed@example.com"
	unknown := signInLog(t, false, typed)
	wrongPassword := signInLog(t, true, typed)

	if unknown != wrongPassword {
		t.Errorf("the two refusals are told apart by the log:\nunknown address:\n%s\nwrong password:\n%s", unknown, wrongPassword)
	}
	for name, out := range map[string]string{"unknown address": unknown, "wrong password": wrongPassword} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(typed)) {
			t.Errorf("%s: the typed address is in the log:\n%s", name, out)
		}
		if strings.Contains(out, "level=ERROR") {
			t.Errorf("%s: a refused sign-in logged at ERROR:\n%s", name, out)
		}
	}
}
