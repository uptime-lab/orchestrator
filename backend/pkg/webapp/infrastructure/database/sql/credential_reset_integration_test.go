package sql

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/credential_reset"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/auth"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/credential_reset"
	two_factor_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/two_factor"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/two_factor"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// The administrative credential reset (the server binary's --reset-password
// and --reset-totp) against a real Postgres and the REAL auth service: what
// it promises is that the old credential stops working and every session
// minted before it dies, and that is a property of the SQL statements and
// the predicates in Issue/Rotate together, which no fake can show.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test -run CredentialReset ./pkg/webapp/infrastructure/database/sql/

type credentialResetFixture struct {
	db      *gorm.DB
	users   *UserRepository
	factors *TwoFactorRepository
	tokens  *RefreshTokenRepository
	audits  *AuditEventRepository
	auth    *auth.AuthService
	reset   *credential_reset.Service
	account *account.Account
}

const credentialResetOldPassword = "the-old-password"

func newCredentialResetFixture(t *testing.T) *credentialResetFixture {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), TranslateError: true})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(8)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	for _, table := range []string{"refresh_tokens", "external_identities", "two_factors", "passkeys", "password_resets",
		"audit_events", "account_members", "role_bindings", "account_invites", "accounts", "users"} {
		if err := db.Exec("DROP TABLE IF EXISTS " + table + " CASCADE").Error; err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &credentialResetFixture{db: db, users: NewUserRepository(db), factors: NewTwoFactorRepository(db),
		tokens: NewRefreshTokenRepository(db), audits: NewAuditEventRepository(db)}
	accounts, members := NewAccountRepository(db), NewAccountMemberRepository(db)
	bindings, invites := NewRoleBindingRepository(db), NewAccountInviteRepository(db)
	identities, passkeys, resets := NewExternalIdentityRepository(db), NewPasskeyRepository(db), NewPasswordResetRepository(db)
	// The TEST migrates; the command under test never does.
	for _, setup := range []func() error{f.users.Setup, accounts.Setup, members.Setup, bindings.Setup, invites.Setup,
		identities.Setup, f.tokens.Setup, f.factors.Setup, passkeys.Setup, resets.Setup, f.audits.Setup} {
		if err := setup(); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
	gate := two_factor_service.NewService(f.factors, f.users, passthroughEncryptor{}, log)
	f.auth = auth.NewAuthService(
		config.JWTConfig{SecretKey: strings.Repeat("k", 64), AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 24 * time.Hour, Issuer: "test"},
		"", 0, auth.OpenRegistration{}, f.users, accounts, members, bindings, invites, identities, f.tokens, gate, passkeys, nil, log)
	hasher := services.NewBCryptPasswordHasher(4)
	f.reset = credential_reset.NewService(f.users, f.factors, f.tokens, resets, members, hasher,
		audit.NewRecorder(f.audits, log), log)

	f.account = account.NewAccount("Lab")
	if err := accounts.Create(context.Background(), f.account); err != nil {
		t.Fatalf("account: %v", err)
	}
	return f
}

func (f *credentialResetFixture) member(t *testing.T, email string) *user.User {
	t.Helper()
	hashed, err := services.NewBCryptPasswordHasher(4).HashPassword(credentialResetOldPassword)
	if err != nil {
		t.Fatal(err)
	}
	u := user.NewUser(email, "Owner", hashed)
	if err := f.users.Create(context.Background(), u); err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := NewAccountMemberRepository(f.db).Create(context.Background(), account_member.NewAccountMember(f.account.ID, u.ID)); err != nil {
		t.Fatalf("membership: %v", err)
	}
	return u
}

func (f *credentialResetFixture) generation(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	u, err := f.users.GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return u.CredentialGeneration
}

func (f *credentialResetFixture) auditRows(t *testing.T, action string) []audit_event.AuditEvent {
	t.Helper()
	rows, _, err := f.audits.List(context.Background(), f.account.ID, repository.AuditEventListFilter{Action: action}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []audit_event.AuditEvent
	for _, r := range rows {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

func TestCredentialResetPasswordEndsTheOldCredentialAndEverySession(t *testing.T) {
	f := newCredentialResetFixture(t)
	ctx := context.Background()
	u := f.member(t, "owner@example.com")

	// A session minted with the old password, before the reset.
	before, err := f.auth.Login("owner@example.com", credentialResetOldPassword, "")
	if err != nil || before.Tokens == nil {
		t.Fatalf("login before the reset: %v %+v", err, before)
	}
	genBefore := f.generation(t, u.ID)

	// Resolved case-insensitively, as the command line passes it.
	res, err := f.reset.ResetPassword(ctx, cmd.ResetPasswordCommand{Email: "  Owner@Example.com"})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if res.TemporaryPassword == "" || !res.SessionsRevoked || res.AuditedAccounts != 1 {
		t.Fatalf("result = %+v", res)
	}

	if _, err := f.auth.Login("owner@example.com", credentialResetOldPassword, ""); err == nil {
		t.Fatal("the old password still signs in")
	}
	if _, err := f.auth.RefreshToken(before.Tokens.RefreshToken); err == nil {
		t.Fatal("a refresh token minted before the reset was accepted")
	}
	if genAfter := f.generation(t, u.ID); genAfter != genBefore+1 {
		t.Fatalf("credential_generation %d -> %d, want +1", genBefore, genAfter)
	}
	var live int64
	if err := f.db.Model(&RefreshTokenModel{}).Where("user_id = ? AND revoked_at IS NULL", u.ID).Count(&live).Error; err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("%d stored sessions survived the reset", live)
	}
	after, err := f.auth.Login("owner@example.com", res.TemporaryPassword, "")
	if err != nil || after.Tokens == nil {
		t.Fatalf("the temporary password does not sign in: %v", err)
	}

	stored, _ := f.users.GetByID(ctx, u.ID)
	rows := f.auditRows(t, audit_event.ActionUserPasswordResetByOperator)
	if len(rows) != 1 {
		t.Fatalf("want one audit row, got %d", len(rows))
	}
	row := rows[0]
	if row.ActorKind != audit_event.ActorSystem || row.ActorUserID != nil || row.TargetID != u.ID.String() {
		t.Fatalf("row = %+v", row)
	}
	for _, secret := range []string{res.TemporaryPassword, stored.HashedPassword, credentialResetOldPassword} {
		if strings.Contains(row.Detail, secret) {
			t.Fatalf("the audit row carries credential material: %q", row.Detail)
		}
	}
}

func TestCredentialResetTOTPRemovesTheFactorAndAdvancesTheGeneration(t *testing.T) {
	f := newCredentialResetFixture(t)
	ctx := context.Background()
	u := f.member(t, "owner@example.com")

	confirmed := time.Now()
	factor := &two_factor.TwoFactor{UserID: u.ID, SecretCiphertext: "SECRETSECRETSECRET", RecoveryCodeHashes: []string{"h1", "h2"},
		ConfirmedAt: &confirmed, CreatedAt: confirmed, UpdatedAt: confirmed}
	if err := f.factors.Upsert(ctx, factor); err != nil {
		t.Fatalf("seeding the factor: %v", err)
	}
	// With the factor, a password alone is a challenge, not a session.
	challenged, err := f.auth.Login("owner@example.com", credentialResetOldPassword, "")
	if err != nil || challenged.TwoFactorToken == "" {
		t.Fatalf("login with a factor must be challenged: %v %+v", err, challenged)
	}
	genBefore := f.generation(t, u.ID)

	res, err := f.reset.ResetTOTP(ctx, cmd.ResetTOTPCommand{Email: "owner@example.com"})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if !res.SessionsRevoked || res.TemporaryPassword != "" {
		t.Fatalf("result = %+v", res)
	}
	if _, err := f.factors.GetByUserID(ctx, u.ID); err == nil {
		t.Fatal("the factor (and its recovery codes) survived")
	}
	if genAfter := f.generation(t, u.ID); genAfter != genBefore+1 {
		t.Fatalf("credential_generation %d -> %d, want +1", genBefore, genAfter)
	}
	// The pending challenge issued before the reset carries the old
	// generation and is refused — the reset reaches it without reaching the
	// server's memory.
	// ErrTokenInvalid, not "invalid code": the epoch is checked before the
	// code, so this asserts the generation refused it.
	if _, err := f.auth.CompleteTwoFactor(ctx, challenged.TwoFactorToken, "000000"); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a two-factor challenge issued before the reset was not refused by its epoch: %v", err)
	}
	// The password is untouched and now opens a session directly.
	if after, err := f.auth.Login("owner@example.com", credentialResetOldPassword, ""); err != nil || after.Tokens == nil {
		t.Fatalf("the password no longer signs in after a TOTP reset: %v %+v", err, after)
	}
	if rows := f.auditRows(t, audit_event.ActionUserTwoFactorResetByOperator); len(rows) != 1 || strings.Contains(rows[0].Detail, "SECRET") {
		t.Fatalf("audit rows = %+v", rows)
	}

	// A second run finds no enabled factor: refused, generation unmoved.
	if _, err := f.reset.ResetTOTP(ctx, cmd.ResetTOTPCommand{Email: "owner@example.com"}); !errors.Is(err, credential_reset.ErrNoEnabledAuthenticator) {
		t.Fatalf("second run: %v", err)
	}
	if g := f.generation(t, u.ID); g != genBefore+1 {
		t.Fatalf("a refused reset moved the generation to %d", g)
	}
}

// A PENDING enrolment is not a factor: the conditional statement leaves it,
// writes nothing and the command refuses.
func TestCredentialResetTOTPLeavesAPendingEnrolment(t *testing.T) {
	f := newCredentialResetFixture(t)
	ctx := context.Background()
	u := f.member(t, "owner@example.com")
	if err := f.factors.CreatePending(ctx, &two_factor.TwoFactor{UserID: u.ID, SecretCiphertext: "PENDING", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	genBefore := f.generation(t, u.ID)
	if _, err := f.reset.ResetTOTP(ctx, cmd.ResetTOTPCommand{Email: "owner@example.com"}); !errors.Is(err, credential_reset.ErrNoEnabledAuthenticator) {
		t.Fatalf("err = %v", err)
	}
	if _, err := f.factors.GetByUserID(ctx, u.ID); err != nil {
		t.Fatalf("the pending enrolment was removed: %v", err)
	}
	if g := f.generation(t, u.ID); g != genBefore {
		t.Fatalf("generation moved %d -> %d", genBefore, g)
	}
}

// Two identities that differ only by case — a row boot-time normalization
// never reached — are an ambiguity, never "the first one".
func TestCredentialResetRefusesAnAmbiguousAddress(t *testing.T) {
	f := newCredentialResetFixture(t)
	ctx := context.Background()
	a := f.member(t, "owner@example.com")
	b := f.member(t, "other@example.com")
	if err := f.db.Exec(`UPDATE users SET email = 'Owner@example.com' WHERE id = ?`, b.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.reset.ResetPassword(ctx, cmd.ResetPasswordCommand{Email: "owner@example.com"}); !errors.Is(err, credential_reset.ErrAmbiguousEmail) {
		t.Fatalf("err = %v", err)
	}
	for _, id := range []uuid.UUID{a.ID, b.ID} {
		if g := f.generation(t, id); g != 1 {
			t.Fatalf("an ambiguous reset wrote: generation %d", g)
		}
	}
}
