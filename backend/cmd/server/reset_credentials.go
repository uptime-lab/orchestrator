package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/credential_reset"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/credential_reset"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/schema"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/database/postgres"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/database/sql"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/logging"
)

// The administrative credential reset, as flags on the server binary for the
// same reason --init-secrets is one: the published image carries only
// /app/server, and `docker compose exec backend /app/server …` runs it with
// the container's own environment — CONFIG_PATH, DB_* and the <VAR>_FILE
// secrets — so it reaches the same database the server does with nothing
// to configure. On a self-hosted install it is the ONLY administrative
// reset: the operator console is outside this tree.
//
// It loads the configuration exactly as the server does, connects, runs NO
// migration (no Setup(): the schema is the running server's, and a one-shot
// must not alter it) and starts no listener.
const (
	resetPasswordFlag = "--reset-password"
	resetTOTPFlag     = "--reset-totp"
	resetEmailFlag    = "--email"
	passwordStdinFlag = "--password-stdin"
)

// maxStdinPassword bounds what --password-stdin reads. bcrypt refuses past
// 72 bytes anyway; the bound only keeps a mistaken pipe of a large file from
// being read whole.
const maxStdinPassword = 4096

const resetUsage = `usage:
  server --reset-password --email ADDRESS [--password-stdin]
  server --reset-totp --email ADDRESS

--reset-password  sets a new password, ends every session of the user and
                  prints a generated temporary password on standard output.
                  With --password-stdin the first line of standard input is
                  the new password instead and nothing is printed. A password
                  is never accepted as an argument: the process list shows
                  arguments to every user of the host.
--reset-totp      removes the user's authenticator app and its recovery codes
                  and ends every session. Passkeys are not touched.

Exit status: 0 done, 1 refused or failed, 2 usage.`

type resetMode int

const (
	resetNone resetMode = iota
	resetPassword
	resetTOTP
)

type resetRequest struct {
	mode          resetMode
	email         string
	passwordStdin bool
	help          bool
}

// The warnings for a reset no audit trail holds. The reset itself is done
// and the exit status stays 0; the operator is told to keep their own
// record. docs/INSTALL.md → "Recover a lost password or authenticator"
// quotes the first sentence.
const (
	noAuditTrailNoMembership = "WARNING: this user is a member of no account, so the reset is recorded in no audit trail; keep your own record of it."
	noAuditTrailUnreadable   = "WARNING: the user's accounts could not be read, so the reset is recorded in no audit trail; keep your own record of it."
)

// errResetUsage marks an argument list the command cannot act on.
var errResetUsage = errors.New("usage")

// parseResetArgs recognises the reset form. present is false for any
// argument list that names neither reset flag — a plain server start.
func parseResetArgs(args []string) (req resetRequest, present bool, err error) {
	for _, a := range args {
		if a == resetPasswordFlag || a == resetTOTPFlag {
			present = true
		}
	}
	if !present {
		return resetRequest{}, false, nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == resetPasswordFlag || a == resetTOTPFlag:
			mode := resetPassword
			if a == resetTOTPFlag {
				mode = resetTOTP
			}
			if req.mode != resetNone {
				return req, true, fmt.Errorf("%w: choose one of %s and %s", errResetUsage, resetPasswordFlag, resetTOTPFlag)
			}
			req.mode = mode
		case a == resetEmailFlag || strings.HasPrefix(a, resetEmailFlag+"="):
			if req.email != "" {
				return req, true, fmt.Errorf("%w: %s given twice", errResetUsage, resetEmailFlag)
			}
			value := strings.TrimPrefix(a, resetEmailFlag+"=")
			if a == resetEmailFlag {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
					return req, true, fmt.Errorf("%w: %s needs an address", errResetUsage, resetEmailFlag)
				}
				i++
				value = args[i]
			}
			req.email = strings.TrimSpace(value)
			if req.email == "" {
				return req, true, fmt.Errorf("%w: %s needs an address", errResetUsage, resetEmailFlag)
			}
		case a == passwordStdinFlag:
			req.passwordStdin = true
		case a == "--help" || a == "-h":
			req.help = true
		case strings.HasPrefix(a, "--password"):
			// --password=… would put the secret in the process list.
			return req, true, fmt.Errorf("%w: a password is never taken as an argument; pipe it with %s", errResetUsage, passwordStdinFlag)
		default:
			return req, true, fmt.Errorf("%w: unexpected argument %q", errResetUsage, a)
		}
	}
	if req.help {
		return req, true, nil
	}
	if req.email == "" {
		return req, true, fmt.Errorf("%w: %s is required", errResetUsage, resetEmailFlag)
	}
	if req.passwordStdin && req.mode != resetPassword {
		return req, true, fmt.Errorf("%w: %s goes with %s only", errResetUsage, passwordStdinFlag, resetPasswordFlag)
	}
	return req, true, nil
}

// readStdinPassword takes the first line of r. Only the line ending is
// stripped: any other whitespace is part of the password, the same rule the
// credential files follow.
func readStdinPassword(r io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(r, maxStdinPassword)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if line == "" {
		return "", errors.New("standard input carried no password")
	}
	return line, nil
}

// credentialResetter is what the command drives; satisfied by
// credential_reset.Service.
type credentialResetter interface {
	ResetPassword(ctx context.Context, c cmd.ResetPasswordCommand) (*results.CredentialResetResult, error)
	ResetTOTP(ctx context.Context, c cmd.ResetTOTPCommand) (*results.CredentialResetResult, error)
}

// resetCredentialsCommand is the dispatch main() calls. It reports whether
// the arguments asked for a reset, and the exit code.
func resetCredentialsCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) (handled bool, code int) {
	req, present, err := parseResetArgs(args)
	if !present {
		return false, 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "%v\n\n%s\n", err, resetUsage)
		return true, 2
	}
	if req.help {
		fmt.Fprintln(stderr, resetUsage)
		return true, 0
	}
	password := ""
	if req.passwordStdin {
		if password, err = readStdinPassword(stdin); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", passwordStdinFlag, err)
			return true, 2
		}
	}

	// Logs go to stderr, so standard output carries the temporary password
	// and nothing else.
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn, ReplaceAttr: logging.RedactHeaderAttr}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	db, closeDB, err := openCredentialResetDatabase(logger)
	if err != nil {
		fmt.Fprintf(stderr, "reset: %v\n", err)
		return true, 1
	}
	defer closeDB()
	return true, resetAgainst(ctx, db, schema.Version, req, password, logger, stdout, stderr)
}

// resetAgainst asks the schema-version ledger first, exactly as the boot
// does, and refuses — before a single write — when a NEWER build migrated
// this database: an older image run with `docker compose run` against an
// upgraded install would otherwise write credential rows under invariants it
// does not know. The refusal carries the boot's own error text.
//
// A missing ledger, or one recording this version or an OLDER one, proceeds.
// The command runs no migration, so "older" means it writes into the schema
// an older server left — which carries every column it writes
// (hashed_password, password_changed_at, credential_generation, the TOTP and
// refresh-token tables) since credential generations were introduced, before
// the ledger existed. A ledger that cannot be read fails closed, as at boot.
func resetAgainst(ctx context.Context, db *gorm.DB, build int, req resetRequest, password string, logger *slog.Logger, stdout, stderr io.Writer) int {
	if _, err := checkSchemaLedger(ctx, sql.NewSchemaLedgerRepository(db), build, logger); err != nil {
		fmt.Fprintf(stderr, "refused: schema-version ledger check failed: %v\n", err)
		return 1
	}
	return runReset(ctx, req, password, newCredentialResetService(db, logger), stdout, stderr)
}

// runReset performs the request against svc and reports. Separate from the
// wiring so the reporting and the exit codes are testable without a
// database.
func runReset(ctx context.Context, req resetRequest, password string, svc credentialResetter, stdout, stderr io.Writer) int {
	var (
		res  *results.CredentialResetResult
		err  error
		what string
	)
	switch req.mode {
	case resetPassword:
		what = "password"
		res, err = svc.ResetPassword(ctx, cmd.ResetPasswordCommand{Email: req.email, Password: password})
	case resetTOTP:
		what = "authenticator app"
		res, err = svc.ResetTOTP(ctx, cmd.ResetTOTPCommand{Email: req.email})
	default:
		fmt.Fprintln(stderr, resetUsage)
		return 2
	}
	if err != nil {
		switch {
		case errors.Is(err, app_errors.ErrUserNotFound):
			fmt.Fprintln(stderr, "refused: no user has this email address; nothing was changed")
		case errors.Is(err, credential_reset.ErrAmbiguousEmail), errors.Is(err, credential_reset.ErrNoEnabledAuthenticator):
			fmt.Fprintf(stderr, "refused: %v\n", err)
		default:
			fmt.Fprintf(stderr, "failed: %v\n", err)
		}
		return 1
	}

	// The temporary password is printed even when the revocation below
	// failed: it is already the user's only password, and withholding it
	// would lock them out for good.
	if res.TemporaryPassword != "" {
		fmt.Fprintln(stdout, res.TemporaryPassword)
	}
	switch {
	case res.AuditedAccounts > 0:
		fmt.Fprintf(stderr, "%s of user %s reset; recorded in the audit trail of %d account(s)\n", what, res.UserID, res.AuditedAccounts)
	case res.MembershipsUnreadable:
		fmt.Fprintf(stderr, "%s of user %s reset\n", what, res.UserID)
		fmt.Fprintln(stderr, noAuditTrailUnreadable)
	default:
		fmt.Fprintf(stderr, "%s of user %s reset\n", what, res.UserID)
		fmt.Fprintln(stderr, noAuditTrailNoMembership)
	}
	if res.TemporaryPassword != "" {
		fmt.Fprintln(stderr, "the temporary password is the one line on standard output and is shown only this once; hand it over and have the user change it after signing in")
	}
	if !res.SessionsRevoked {
		fmt.Fprintln(stderr, "failed: revoking the user's stored sessions did not complete; the credential generation has advanced, so they cannot refresh. Run the command again to retry the revocation.")
		return 1
	}
	fmt.Fprintln(stderr, "every stored session of the user has been revoked")
	return 0
}

// openCredentialResetDatabase loads the configuration as the server does
// and opens a fresh connection. Statement logging is silenced: in a
// development mode GORM logs every statement with its values, which for
// this command means the new password hash on the operator's terminal.
func openCredentialResetDatabase(logger *slog.Logger) (*gorm.DB, func(), error) {
	cfg, err := loadConfig(logger)
	if err != nil {
		return nil, nil, fmt.Errorf("loading the configuration: %w", err)
	}
	db, err := postgres.NewDatabaseConnection(cfg.Database, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to the database: %w", err)
	}
	db.Logger = db.Logger.LogMode(gormlogger.Silent)
	closeDB := func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(2)
	}
	return db, closeDB, nil
}

// newCredentialResetService wires the service over db. No repository's
// Setup() is called.
func newCredentialResetService(db *gorm.DB, logger *slog.Logger) *credential_reset.Service {
	return credential_reset.NewService(
		sql.NewUserRepository(db),
		sql.NewTwoFactorRepository(db),
		sql.NewRefreshTokenRepository(db),
		sql.NewPasswordResetRepository(db),
		sql.NewAccountMemberRepository(db),
		services.NewBCryptPasswordHasher(0),
		audit_service.NewRecorder(sql.NewAuditEventRepository(db), logger),
		logger,
	)
}
