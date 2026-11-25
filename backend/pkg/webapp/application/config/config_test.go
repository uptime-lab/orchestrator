package config

import (
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

func baseProdConfig() *Config {
	c := getDefaultConfig()
	c.Server.Environment = EnvProduction
	// What `openssl rand -hex 32` produces, which is what a real deployment
	// holds. The previous fixtures ("a-strong-unique-secret") were 22 characters
	// and would now be refused — deliberately, since a phrase that short is a
	// human-typed one.
	c.JWT.SecretKey = "8f14e45fceea167a5a36dedd4bea2543f14e45fceea167a5a36dedd4bea25431"
	c.Secrets.EncryptionKey = "c4ca4238a0b923820dcc509a6f75849bca4238a0b923820dcc509a6f75849bcd"
	// Mandatory in production (cdn_signing_key_test.go).
	c.Secrets.CDNSigningKey = "e10adc3949ba59abbe56e057f20f883ee10adc3949ba59abbe56e057f20f883e"
	c.GRPC.TLSCertFile = "/tls/cert.pem"
	c.GRPC.TLSKeyFile = "/tls/key.pem"
	c.Server.InviteCode = "beta-secret"
	c.Server.CORS.AllowedOrigins = []string{"https://app.example.com"}
	return c
}

// An empty invite code is no longer "open to anyone": from the first account
// on the auth service reads it as CLOSED, and the empty install admits its
// first owner without one — so production has nothing to refuse here.
func TestValidateProductionAcceptsAnEmptyInviteCode(t *testing.T) {
	c := baseProdConfig()
	c.Server.InviteCode = ""
	if err := c.Validate(); err != nil {
		t.Fatalf("an empty invite code means closed registration, not an insecure boot: %v", err)
	}
	for _, p := range c.Problems() {
		if strings.Contains(p, "SERVER_INVITE_CODE") {
			t.Fatalf("Problems() still lists the invite code: %q", p)
		}
	}
}

func TestValidateProductionRejectsWildcardCORSWithCredentials(t *testing.T) {
	c := baseProdConfig()
	c.Server.CORS.AllowedOrigins = []string{"*"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected production validation to reject wildcard CORS with credentials")
	}
}

func TestValidateDevelopmentAllowsInsecureDefaults(t *testing.T) {
	c := getDefaultConfig()
	c.Server.Environment = "development" // opting out must be explicit ...
	c.Server.AllowInsecureDevSecrets = true // ... twice: the secret checks refuse everywhere without this
	if err := c.Validate(); err != nil {
		t.Fatalf("development mode should not fail validation: %v", err)
	}
}

// The whole point of the inverted gate: saying nothing must land in the
// hardened mode, not the loose one. Before this, an unset APP_ENV silently
// shipped the publicly known built-in JWT secret.
func TestValidateUnsetEnvironmentIsTreatedAsProduction(t *testing.T) {
	c := getDefaultConfig()
	if !c.IsProduction() {
		t.Fatal("an unset environment must be treated as production")
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected insecure defaults to be rejected when the environment is unset")
	}
}

// A typo must fail closed rather than silently disable every guard.
func TestValidateMisspelledEnvironmentIsTreatedAsProduction(t *testing.T) {
	c := getDefaultConfig()
	c.Server.Environment = "developement"
	if !c.IsProduction() {
		t.Fatal("an unrecognized environment must be treated as production")
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected insecure defaults to be rejected for an unrecognized environment")
	}
}

func TestInsecureEnvironmentAliasesAreRecognized(t *testing.T) {
	for _, name := range []string{"development", "dev", "local", "test", "DEV", " Development "} {
		c := getDefaultConfig()
		c.Server.Environment = name
		if c.IsProduction() {
			t.Errorf("environment %q should opt out of fail-closed validation", name)
		}
	}
}

func TestValidateProductionRejectsDefaultJWTSecret(t *testing.T) {
	c := baseProdConfig()
	c.JWT.SecretKey = insecureDefaultJWTSecret
	if err := c.Validate(); err == nil {
		t.Fatal("expected production validation to reject the default JWT secret")
	}
}

// The old check compared against ONE literal, so every placeholder this
// repository ships except that one passed — including the example config's own
// value, which a deployment that mounts its config directory would boot on.
func TestValidateProductionRejectsEveryPlaceholderSecretThisRepoShips(t *testing.T) {
	shipped := map[string]string{
		"config/config.yaml.example": "your-secret-key-change-in-production",
		"config_local/config.yaml":   "local-dev-secret",
		"built-in default":           insecureDefaultJWTSecret,
		"a common placeholder":       "changeme-changeme-changeme-changeme",
		"the dev encryption key":     InsecureDefaultSecretsKey,
	}
	for source, secret := range shipped {
		c := baseProdConfig()
		c.JWT.SecretKey = secret
		if err := c.Validate(); err == nil {
			t.Errorf("accepted the placeholder secret from %s (%q)", source, secret)
		}
	}
}

// A secret long enough to look generated but short enough to have been typed is
// the case a marker list cannot catch.
func TestValidateProductionRejectsAShortSecret(t *testing.T) {
	c := baseProdConfig()
	c.JWT.SecretKey = "hunter2hunter2hunter2" // 21 chars, no marker
	err := c.Validate()
	if err == nil {
		t.Fatal("accepted a 21-character JWT secret in production")
	}
	// The message has to say WHY, or an operator reads a refused boot as a bug.
	if !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("error does not name the floor: %v", err)
	}
}

// The same rules apply to the encryption key: it was checked for emptiness only,
// so the built-in insecure development key passed when set explicitly.
func TestValidateProductionRejectsAWeakEncryptionKey(t *testing.T) {
	for _, key := range []string{InsecureDefaultSecretsKey, "short-key"} {
		c := baseProdConfig()
		c.Secrets.EncryptionKey = key
		if err := c.Validate(); err == nil {
			t.Errorf("accepted encryption key %q in production", key)
		}
	}
}

// Whitespace is not a secret. A file-mounted credential ending in a newline must
// not read as 33 acceptable characters when 32 of them are blank.
func TestValidateProductionIgnoresSurroundingWhitespace(t *testing.T) {
	c := baseProdConfig()
	c.JWT.SecretKey = "                                 \n"
	if err := c.Validate(); err == nil {
		t.Fatal("accepted whitespace as a JWT secret")
	}
}

// A development install must stay usable with the built-in defaults: these
// checks are production-only by design, and such an install runs with
// APP_ENV=development.
// The secret rules apply outside production too; what development gets is
// the opt-in, not an exemption (insecure_dev_secrets_test.go has the rest).
func TestTheSecretRulesApplyOutsideProductionUntilOptedOut(t *testing.T) {
	c := getDefaultConfig()
	c.Server.Environment = "development"
	c.JWT.SecretKey = "local-dev-secret"
	if err := c.Validate(); err == nil {
		t.Fatal("development ran on a placeholder secret without ALLOW_INSECURE_DEV_SECRETS")
	}
	c.Server.AllowInsecureDevSecrets = true
	if err := c.Validate(); err != nil {
		t.Fatalf("development with the opt-in must tolerate the local secret: %v", err)
	}
}

func TestValidateProductionRejectsEmptyEncryptionKey(t *testing.T) {
	c := baseProdConfig()
	c.Secrets.EncryptionKey = ""
	if err := c.Validate(); err == nil {
		t.Fatal("expected production validation to reject an empty encryption key")
	}
}

func TestValidateProductionRejectsPlaintextGRPC(t *testing.T) {
	c := baseProdConfig()
	c.GRPC.TLSCertFile = ""
	if err := c.Validate(); err == nil {
		t.Fatal("expected production validation to reject plaintext gRPC")
	}
}

func TestValidateProductionAcceptsSecureConfig(t *testing.T) {
	if err := baseProdConfig().Validate(); err != nil {
		t.Fatalf("secure production config should pass: %v", err)
	}
}

func TestValidateProductionAcceptsExternallyTerminatedTLS(t *testing.T) {
	c := baseProdConfig()
	// No backend cert files, but TLS is terminated by an upstream proxy.
	c.GRPC.TLSCertFile = ""
	c.GRPC.TLSKeyFile = ""
	c.GRPC.TLSTerminatedExternally = true
	if err := c.Validate(); err != nil {
		t.Fatalf("externally-terminated TLS should satisfy validation: %v", err)
	}
}

// The config package deliberately holds no domain import, so the tracked-file
// defaults are written out twice — here and in node_file.DefaultLimits(). This
// pins them together: a limit changed in one place and not the other would
// otherwise make the shipped default depend on which one an install reads.
func TestNodeFileDefaultsMatchTheDomain(t *testing.T) {
	cfg := getDefaultConfig()
	want := node_file.DefaultLimits()

	if cfg.NodeFile.MaxVersionsPerPath != want.VersionsPerPath {
		t.Errorf("versions per path: config %d, domain %d", cfg.NodeFile.MaxVersionsPerPath, want.VersionsPerPath)
	}
	if cfg.NodeFile.MaxFilesPerNode != want.FilesPerNode {
		t.Errorf("files per node: config %d, domain %d", cfg.NodeFile.MaxFilesPerNode, want.FilesPerNode)
	}
	if cfg.NodeFile.MaxBytesPerNode != want.BytesPerNode {
		t.Errorf("bytes per node: config %d, domain %d", cfg.NodeFile.MaxBytesPerNode, want.BytesPerNode)
	}
	// The account ceiling is the one that starts unlimited: an install that has
	// not chosen a number should not have one imposed.
	if cfg.NodeFile.MaxBytesPerAccount != 0 || want.BytesPerAccount != 0 {
		t.Error("the per-account ceiling must default to unlimited")
	}
}

// IsProductionEnvironment is what callers that run BEFORE the config exists use
// — chiefly setupLogger, which needs a level before there is a Config to ask.
// It must agree with Config.IsProduction in every case, because setupLogger
// previously carried its own `!= "production"` compare (the pre-inversion
// polarity) and so gave Debug-level logging on an install the rest of the
// config treated as production.
func TestIsProductionEnvironmentAgreesWithTheConfigMethod(t *testing.T) {
	for _, name := range []string{
		"", "production", "Production", "  production  ", "prod",
		"development", "DEVELOPMENT", " dev ", "local", "test",
		"staging", "developement", "produciton", "anything-else",
	} {
		cfg := &Config{}
		cfg.Server.Environment = name

		if got, want := IsProductionEnvironment(name), cfg.IsProduction(); got != want {
			t.Errorf("environment %q: IsProductionEnvironment=%v but Config.IsProduction=%v", name, got, want)
		}
	}
}

// The inversion is the whole point: only the four recognized insecure names opt
// out. A typo must not silently unlock development behaviour.
func TestIsProductionEnvironmentTreatsTyposAndUnsetAsProduction(t *testing.T) {
	insecure := []string{"development", "dev", "local", "test"}
	for _, name := range insecure {
		if IsProductionEnvironment(name) {
			t.Errorf("%q should opt out of production", name)
		}
	}
	for _, name := range []string{"", "produciton", "developement", "staging", "qa", "PROD"} {
		if !IsProductionEnvironment(name) {
			t.Errorf("%q must be treated as production", name)
		}
	}
}

// The fail-closed checks used to be unreachable on a development install,
// because they sat behind an early return on a non-production environment.
// Problems() must compute them regardless, so such an install can log what
// production would refuse.
func TestProblemsAreComputedOutsideProductionToo(t *testing.T) {
	c := getDefaultConfig()
	c.Server.Environment = "development"

	problems := c.Problems()
	if len(problems) == 0 {
		t.Fatal("a development config with every insecure default reported no problems; the checks are unreachable again")
	}
	// Validate must still tolerate the NON-secret part of it: a development
	// install genuinely serves plain HTTP on a trusted LAN, and refusing to
	// boot it is not the fix. The secret part needs the opt-in.
	c.Server.AllowInsecureDevSecrets = true
	if err := c.Validate(); err != nil {
		t.Fatalf("development must not be refused: %v", err)
	}
}

// The same list is what production refuses, so the two cannot disagree about
// what counts as insecure.
func TestValidateRefusesExactlyWhatProblemsReports(t *testing.T) {
	insecure := getDefaultConfig()
	insecure.Server.Environment = EnvProduction
	problems := insecure.Problems()
	err := insecure.Validate()
	if err == nil {
		t.Fatal("production accepted a config with every insecure default")
	}
	for _, problem := range problems {
		if !strings.Contains(err.Error(), problem) {
			t.Errorf("the production error omits a reported problem: %q", problem)
		}
	}

	secure := baseProdConfig()
	if got := secure.Problems(); len(got) != 0 {
		t.Fatalf("a secure production config reported problems: %v", got)
	}
	if err := secure.Validate(); err != nil {
		t.Fatalf("a secure production config was refused: %v", err)
	}
}
