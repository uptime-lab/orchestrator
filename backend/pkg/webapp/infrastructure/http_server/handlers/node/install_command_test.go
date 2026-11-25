package node

import "testing"

// The served install.sh refuses to fetch adoption credentials from a
// plaintext platform URL unless it is given --insecure, so the command handed
// out on an install that advertises http:// must carry the flag, and one on
// https:// must not.
func TestAdoptionInstallCommandCarriesInsecureExactlyOnPlaintext(t *testing.T) {
	for _, c := range []struct{ platform, want string }{
		{"https://orchestrator.example.com", "curl -fsSL https://orchestrator.example.com/install.sh | sudo sh -s -- --token 'ac_1'"},
		{"https://orchestrator.example.com/", "curl -fsSL https://orchestrator.example.com/install.sh | sudo sh -s -- --token 'ac_1'"},
		{"http://lan.example.com", "curl -fsSL http://lan.example.com/install.sh | sudo sh -s -- --token 'ac_1' --insecure"},
		{"lan.example.com", "curl -fsSL http://lan.example.com/install.sh | sudo sh -s -- --token 'ac_1' --insecure"},
	} {
		if got := adoptionInstallCommand(c.platform, "ac_1"); got != c.want {
			t.Errorf("%q:\n got %s\nwant %s", c.platform, got, c.want)
		}
	}
}
