package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

// initSecretsFlag is the one-shot form of the server binary: it writes the
// credential files a fresh install mounts and exits, before any configuration
// is loaded or any database is touched. It is a flag on THIS binary rather
// than a separate command because the published image carries only
// /app/server, and the installation page runs it from that image so the files
// come out owned by the uid the containers read them with.
const initSecretsFlag = "--init-secrets"

// initSecretsCommand handles `--init-secrets DIR` (or `--init-secrets=DIR`).
// It reports whether the arguments asked for it and the exit code to use;
// a plain server start is any other argument list.
func initSecretsCommand(args []string, stdout, stderr io.Writer) (handled bool, code int) {
	dir, ok := initSecretsDir(args)
	if !ok {
		return false, 0
	}
	if dir == "" {
		fmt.Fprintf(stderr, "usage: server %s DIR\n", initSecretsFlag)
		return true, 2
	}
	report, err := config.InitSecrets(dir)
	if err != nil {
		if errors.Is(err, config.ErrSecretFileExists) {
			fmt.Fprintf(stderr, "%v\nThe bootstrap never overwrites: a replaced secrets_encryption_key makes every encrypted row unreadable. Remove the directory yourself if it is a false start, or leave it alone if the server has ever booted on it.\n", err)
			return true, 1
		}
		fmt.Fprintf(stderr, "init-secrets: %v\n", err)
		return true, 1
	}
	fmt.Fprintf(stdout, "wrote %d credential files into %s (directory 0700, files 0400, owned by the user running this):\n", len(report.Files), report.Dir)
	for _, f := range report.Files {
		state := "generated"
		if !f.Generated {
			state = "empty    "
		}
		fmt.Fprintf(stdout, "  %-28s %s  %s\n", f.File, state, f.Purpose)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Point the server at them with these variables (the compose file already does, at /run/secrets/<name>):")
	for _, line := range report.EnvLines() {
		fmt.Fprintf(stdout, "  %s\n", line)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Back up secrets_encryption_key now, before the first boot.")
	return true, 0
}

// initSecretsDir extracts the directory. The flag is accepted anywhere in
// the arguments, in both spellings; a flag with no value asks for usage.
func initSecretsDir(args []string) (dir string, present bool) {
	for i, a := range args {
		switch {
		case a == initSecretsFlag:
			if i+1 < len(args) {
				return strings.TrimSpace(args[i+1]), true
			}
			return "", true
		case strings.HasPrefix(a, initSecretsFlag+"="):
			return strings.TrimSpace(strings.TrimPrefix(a, initSecretsFlag+"=")), true
		}
	}
	return "", false
}
