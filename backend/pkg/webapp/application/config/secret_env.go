package config

import (
	"log/slog"
	"os"
	"strings"
)

// secretEnv reads a credential from a FILE when one is named, falling back to
// the plain environment variable.
//
// `FOO_FILE=/run/secrets/foo` wins over `FOO`. This is the convention the
// official Postgres image and Docker secrets already use, so one mechanism
// covers our services and the images we do not write.
//
// Why the file is better, given that root can read either: a container's whole
// environment is exposed by `docker inspect` and by `/proc/<pid>/environ`,
// which means it leaks through paths that have nothing to do with a compromise
// — an inspect output pasted into an issue, a crash reporter, a process listing
// shared while debugging. A Docker API proxy that answers `inspect` hands out
// every container's environment; isolating such a proxy closes the route, this
// closes the value.
//
// The environment fallback is kept deliberately. A deployment that still holds
// its secrets in a `.env` file must keep working, and a change that broke that
// arrangement would have to be applied to every service on every host in one
// step — which is how a control plane ends up down at midnight. Migration is
// therefore per-variable and reversible.
func secretEnv(name string) string {
	if path := strings.TrimSpace(os.Getenv(name + "_FILE")); path != "" {
		value, err := os.ReadFile(path)
		if err != nil {
			// Loud, and NOT a fallback to the environment variable. A named file
			// is an explicit statement about where the credential lives; quietly
			// using a stale environment copy instead would leave the operator
			// believing a rotation had taken effect when it had not.
			slog.Error("failed to read a credential from its file; treating it as unset",
				"variable", name, "file", path, "error", err)
			return ""
		}
		// Trailing newlines are what every editor and `echo` adds, and a
		// credential with one is a credential that does not match.
		return strings.TrimRight(string(value), "\r\n")
	}
	return os.Getenv(name)
}
