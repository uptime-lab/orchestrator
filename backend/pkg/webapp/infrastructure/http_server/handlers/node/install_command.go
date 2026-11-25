package node

import (
	"fmt"
	"strings"
)

// adoptionInstallCommand renders the one-command install line for a one-time
// adoption code. The served install.sh refuses to fetch adoption credentials
// from a plaintext platform URL unless it is given --insecure; an install that
// advertises http:// has already declared that it means it (a server-side
// setting), so the command it hands out carries the flag and runs as shown. A
// scheme-less advertisement URL is http, as everywhere else on the platform.
func adoptionInstallCommand(platformURL, code string) string {
	base := strings.TrimRight(platformURL, "/")
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	insecure := ""
	if strings.HasPrefix(strings.ToLower(base), "http://") {
		insecure = " --insecure"
	}
	return fmt.Sprintf("curl -fsSL %s/install.sh | sudo sh -s -- --token '%s'%s", base, code, insecure)
}
