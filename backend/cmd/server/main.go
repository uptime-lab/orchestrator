package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/uptime-lab/orchestrator/backend/docs" // This imports the generated docs
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	access_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/access"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/account"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/auth"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/credlock"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/eeprom_provision"
	email_verification_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/email_verification"
	entitlement_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/entitlement"
	image_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/image"
	invite_delivery_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/invite_delivery"
	master_token_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/master_token"
	member_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/member"
	node_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node"
	node_activity_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_activity"
	node_attention_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_attention"
	node_command_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_command"
	node_favorite_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_favorite"
	node_file_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_file"
	node_replace_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_replace"
	node_ssh_adopt_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_ssh_adopt"
	organizational_unit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/organizational_unit"
	passkey_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/passkey"
	password_reset_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/password_reset"
	reauth_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/reauth"
	retention_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/retention"
	secret_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/secret"
	secret_tag_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/secret_tag"
	ssh_key_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/ssh_key"
	two_factor_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/two_factor"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/user"
	utility_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/utility"
	workflow_execution_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_execution"
	workflow_template_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_template"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/buildinfo"
	node_file_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/schema"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/agentrelease"
	oidcapi "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/oidc"
	gcp_storage "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/gcp"
	local_storage "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/local"
	s3_storage "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/s3"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
	compression_infra "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/compression"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/crypto"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/database/postgres"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/database/sql"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/device_profile_registry"
	eepromsigner "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/eepromsigner"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/health"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/logging"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/mailer"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/node_commands"
	ssh_adapter "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/ssh"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/tmpsweep"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/utility_registry"
)

const (
	shutdownTimeout = 10 * time.Second
	configPath      = "../config_local/config.yaml"
)

func main() {
	// The one-shot secret bootstrap runs before anything else: it needs no
	// configuration (it is what produces it) and must not touch a database.
	if handled, code := initSecretsCommand(os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
	// The release import loads the configuration and opens the storage
	// backend, nothing else: no database, no listeners.
	if handled, code := importAgentReleaseCommand(os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
	// The administrative credential reset: loads the configuration and
	// connects, but migrates nothing and listens on nothing. It asks the
	// schema-version ledger itself before any write (resetAgainst), so it
	// may sit above the boot's ledger check. Neither command above opens
	// the database.
	if handled, code := resetCredentialsCommand(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}

	// Initialize logger
	logger := setupLogger()
	// What the capabilities endpoint reports as this build's revision and
	// platform version.
	buildinfo.SetRevision(buildRevision())
	buildinfo.SetVersion(buildVersion())
	logger.Info("starting application")

	// Load configuration
	cfg, err := loadConfig(logger)
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// Initialize database
	db, err := postgres.NewDatabaseConnection(cfg.Database, logger)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	logger.Info("database connection established")

	// SCHEMA-VERSION LEDGER: THIS MUST STAY BEFORE THE FIRST Setup(). A
	// database migrated by a newer release is refused HERE, before any
	// AutoMigrate has run, so the refusal leaves it exactly as the newer
	// release left it; moved below any Setup() the same check would refuse a
	// database it had already altered. A ledger read error fails closed; only
	// a missing table (a fresh or pre-ledger database) proceeds.
	// TestSchemaLedgerCheckRunsBeforeAnySetup enforces the order.
	schemaLedgerRepo := sql.NewSchemaLedgerRepository(db)
	schemaLedgerBefore, err := checkSchemaLedger(context.Background(), schemaLedgerRepo, schema.Version, logger)
	if err != nil {
		logger.Error("refusing to start: schema-version ledger check failed",
			"build_schema_version", schema.Version, "build_revision", buildRevision(), "error", err)
		os.Exit(1)
	}
	// The ledger row is written NOW, before the first product Setup(), with
	// migrated=false: a newer build whose migrations then fail part-way still
	// leaves a row that refuses an older build. The ledger table's own DDL is
	// the only statement between the decision above and the migrations.
	if err := recordSchemaIntent(context.Background(), schemaLedgerRepo, schemaLedgerRepo.Setup,
		schemaLedgerBefore, schema.Version, buildRevision(), logger); err != nil {
		logger.Error("refusing to start: could not record the schema-version ledger before migrating", "error", err)
		os.Exit(1)
	}

	// Initialize repositories
	logger.Info("initializing repositories")
	userRepo := sql.NewUserRepository(db)
	accountRepo := sql.NewAccountRepository(db)
	// Secret encryption: derive a 32-byte AES-256 key from the configured key.
	// If unset, fall back to an insecure fixed dev key so local dev works.
	// Validate() has already rejected an empty key in EVERY mode unless a
	// development mode opted in with ALLOW_INSECURE_DEV_SECRETS, so this
	// fallback is reached only by an install that said so. Shared by the
	// secret repo and the SSH-key repo (connection keypairs' private keys).
	secretsKey := cfg.Secrets.EncryptionKey
	if secretsKey == "" {
		logger.Warn("SECRETS_ENCRYPTION_KEY is not set; using an INSECURE fixed development key for secret encryption. Set secrets.encryption_key / SECRETS_ENCRYPTION_KEY in production.")
		secretsKey = config.InsecureDefaultSecretsKey
	}
	// Old keys (SECRETS_ENCRYPTION_KEY_OLD) are accepted for decryption during a
	// rotation; new data is always encrypted under the primary key.
	secretEncryptor := crypto.NewEncryptorWithFallbacks(secretsKey, cfg.Secrets.OldEncryptionKeys)
	if len(cfg.Secrets.OldEncryptionKeys) > 0 {
		logger.Info("secret encryption running with fallback (old) keys for rotation", "fallback_count", len(cfg.Secrets.OldEncryptionKeys))
	}

	nodeRepo := sql.NewNodeRepository(db, secretEncryptor)
	imageRepo := sql.NewImageRepository(db)
	nodeFileRepo := sql.NewNodeFileRepository(db, secretEncryptor)
	nodeTrackedFileRepo := sql.NewNodeTrackedFileRepository(db)
	sshKeyRepo := sql.NewSSHKeyRepository(db, secretEncryptor)
	nodeCommandRepo := sql.NewNodeCommandRepository(db, secretEncryptor)
	workflowTemplateRepo := sql.NewWorkflowTemplateRepository(db)
	workflowExecutionRepo := sql.NewWorkflowExecutionRepository(db)
	workflowExecutionNodeRepo := sql.NewWorkflowExecutionNodeRepository(db, secretEncryptor)
	masterTokenRepo := sql.NewMasterTokenRepository(db, secretEncryptor)
	adoptionCodeRepo := sql.NewAdoptionCodeRepository(db)
	emailVerificationRepo := sql.NewEmailVerificationRepository(db)
	passwordResetRepo := sql.NewPasswordResetRepository(db)
	refreshTokenRepo := sql.NewRefreshTokenRepository(db)
	twoFactorRepo := sql.NewTwoFactorRepository(db)
	passkeyRepo := sql.NewPasskeyRepository(db)
	instanceAnomalyRepo := sql.NewInstanceAnomalyRepository(db)
	// Say, once at startup, when nothing reads what the detectors are about to
	// write.
	//
	// The backend detects and records unconditionally by design — alerting
	// policy belongs to an operator console outside this tree, and the backend
	// never reads that tooling's settings, so the dependency stays one-way and
	// the tooling can run with its own, narrower access. The consequence on an
	// install with no such console is that anomalies accumulate in a table
	// nothing reads, silently and forever. This is what makes that a STATED
	// condition.
	//
	// It cannot be derived: the backend cannot see whether a reader exists. So
	// the default is the safe direction — an install that says nothing is
	// assumed to have no reader.
	if cfg.AnomalyReaderDeclaredAbsent() {
		logger.Info("instance anomalies are recorded in instance_anomalies; this install declares that nothing reads them (INSTANCE_MONITORING_READER=none)")
	} else if !cfg.AnomaliesAreRead() {
		logger.Warn("instance anomalies are recorded but nothing on this install reads them; " +
			"the detectors will keep upserting into instance_anomalies with no alerting behind it. " +
			"Set INSTANCE_MONITORING_READER=admin-console on an install running the admin console, or =none to state that nothing does")
	}
	instanceHeartbeatRepo := sql.NewInstanceHeartbeatRepository(db)
	organizationalUnitRepo := sql.NewOrganizationalUnitRepository(db, secretEncryptor)
	// Guarded OU mutations (delete-vs-assign serialization) live in their own
	// repository so the racy check-then-act pieces cannot be recomposed.
	ouMutationRepo := sql.NewOUMutationRepository(db)
	accountPolicyRepo := sql.NewAccountPolicyRepository(db, secretEncryptor)
	nodeFavoriteRepo := sql.NewNodeFavoriteRepository(db)
	// What a deleted node WAS, so its device can be taken back. See
	// domain/node_tombstone for why a delete otherwise strands the device.
	nodeTombstoneRepo := sql.NewNodeTombstoneRepository(db, secretEncryptor)
	accountDeletionRequestRepo := sql.NewAccountDeletionRequestRepository(db)
	// The entitlement files applied to this install. Instance-wide: no
	// account_id, so no account's deletion touches it.
	instanceEntitlementRepo := sql.NewInstanceEntitlementRepository(db)
	adoptionJobRepo := sql.NewNodeAdoptionJobRepository(db)
	accountMemberRepo := sql.NewAccountMemberRepository(db)
	roleBindingRepo := sql.NewRoleBindingRepository(db)
	// Guarded membership mutations: the last-super_admin invariant is enforced
	// inside the mutation transaction under a per-account advisory lock.
	memberMutationRepo := sql.NewMemberMutationRepository(db)
	accountInviteRepo := sql.NewAccountInviteRepository(db)
	accountRoleSettingRepo := sql.NewAccountRoleSettingRepository(db)
	auditEventRepo := sql.NewAuditEventRepository(db)
	externalIdentityRepo := sql.NewExternalIdentityRepository(db)

	secretRepo := sql.NewSecretRepository(db, secretEncryptor)
	secretAttachmentRepo := sql.NewSecretAttachmentRepository(db)
	secretTagRepo := sql.NewSecretTagRepository(db)
	accessEntryRepo := sql.NewAccessEntryRepository(db, secretEncryptor)
	utilityAssignmentRepo := sql.NewUtilityAssignmentRepository(db)
	nodeUtilityStateRepo := sql.NewNodeUtilityStateRepository(db)

	// Run database migrations
	logger.Info("running database migrations")
	if err := userRepo.Setup(); err != nil {
		logger.Error("failed to setup user repository", "error", err)
		os.Exit(1)
	}
	if err := accountRepo.Setup(); err != nil {
		logger.Error("failed to setup account repository", "error", err)
		os.Exit(1)
	}
	if err := nodeRepo.Setup(); err != nil {
		logger.Error("failed to setup node repository", "error", err)
		os.Exit(1)
	}
	if err := imageRepo.Setup(); err != nil {
		logger.Error("failed to setup image repository", "error", err)
		os.Exit(1)
	}
	if err := nodeFileRepo.Setup(); err != nil {
		logger.Error("failed to setup node file repository", "error", err)
		os.Exit(1)
	}
	if renumbered, err := nodeFileRepo.EnsureVersionUniqueIndex(); err != nil {
		// Non-fatal for the same reason the backfill above is: this closes a race
		// that has to be lost to matter, and a table that still holds a tie is one
		// the platform serves correctly today. Refusing to boot over it would turn
		// a latent duplicate into an outage.
		logger.Error("failed to enforce unique file versions; concurrent saves of one path can still collide", "error", err)
	} else if renumbered > 0 {
		logger.Info("renumbered duplicate file versions", "rows", renumbered)
	}
	if converted, err := nodeFileRepo.BackfillContentEncryption(); err != nil {
		// Non-fatal: unencrypted rows still serve correctly, and crash-looping a
		// live install over a hygiene backfill is a failure mode worth refusing.
		logger.Error("failed to encrypt existing node file content; rows remain in plaintext", "converted", converted, "error", err)
	} else if converted > 0 {
		logger.Info("encrypted existing node file content", "rows", converted)
	}
	if repaired, err := nodeFileRepo.RepairMislabeledContent(); err != nil {
		// Non-fatal for the same reason: the read path serves these rows either
		// way. A count above zero is worth reading as what it is — evidence that a
		// backend without the decryption code was served over encrypted rows.
		logger.Error("failed to repair node file rows marked encrypted but holding plaintext", "repaired", repaired, "error", err)
	} else if repaired > 0 {
		logger.Warn("repaired node file rows that were marked encrypted but held plaintext; a backend without content decryption was deployed over encrypted rows at some point", "rows", repaired)
	}
	if err := nodeTrackedFileRepo.Setup(); err != nil {
		// Non-fatal on purpose. The table migration is additive and the backfill
		// only adopts existing paths, so a failure means "the Files list is
		// incomplete until the next start", not "this deployment is broken".
		// A fatal migration here would crash-loop a live install over bookkeeping.
		logger.Error("failed to setup node tracked file repository; the tracked-file list may be incomplete", "error", err)
	}
	if err := sshKeyRepo.Setup(); err != nil {
		logger.Error("failed to setup SSH key repository", "error", err)
		os.Exit(1)
	}
	if err := nodeCommandRepo.Setup(); err != nil {
		logger.Error("failed to setup node command repository", "error", err)
		os.Exit(1)
	}
	// Separate from Setup() and NON-fatal, unlike the schema migration above. It
	// sweeps node_commands, which has no retention and is the largest table in
	// the schema, so making it fatal meant a hygiene backfill could crash-loop a
	// live install under `restart: always` with no healthcheck to stop it.
	// Unencrypted rows serve correctly in the meantime.
	if err := nodeCommandRepo.BackfillEncryption(); err != nil {
		logger.Error("failed to encrypt legacy node-command payloads; rows remain in plaintext", "error", err)
	}
	// Same contract: non-fatal. Seeds nodes.bootloader_flashed_at from the
	// command log for flashes that settled before the column existed; the
	// gRPC settle hook stamps it from here on.
	if err := nodeCommandRepo.BackfillBootloaderFlashedAt(); err != nil {
		logger.Error("failed to backfill bootloader flash times; pending-reboot state starts from the next flash", "error", err)
	}
	if err := workflowTemplateRepo.Setup(); err != nil {
		logger.Error("failed to setup workflow template repository", "error", err)
		os.Exit(1)
	}
	if err := workflowExecutionRepo.Setup(); err != nil {
		logger.Error("failed to setup workflow execution repository", "error", err)
		os.Exit(1)
	}
	if err := workflowExecutionNodeRepo.Setup(); err != nil {
		logger.Error("failed to setup workflow execution node repository", "error", err)
		os.Exit(1)
	}
	if err := masterTokenRepo.Setup(); err != nil {
		logger.Error("failed to setup master token repository", "error", err)
		os.Exit(1)
	}
	if err := adoptionCodeRepo.Setup(); err != nil {
		logger.Error("failed to setup adoption code repository", "error", err)
		os.Exit(1)
	}
	if err := emailVerificationRepo.Setup(); err != nil {
		logger.Error("failed to setup email verification repository", "error", err)
		os.Exit(1)
	}
	if err := passwordResetRepo.Setup(); err != nil {
		logger.Error("failed to setup password reset repository", "error", err)
		os.Exit(1)
	}
	if err := refreshTokenRepo.Setup(); err != nil {
		logger.Error("failed to setup refresh token repository", "error", err)
		os.Exit(1)
	}
	if err := twoFactorRepo.Setup(); err != nil {
		logger.Error("failed to setup two-factor repository", "error", err)
		os.Exit(1)
	}
	if err := passkeyRepo.Setup(); err != nil {
		logger.Error("failed to setup passkey repository", "error", err)
		os.Exit(1)
	}
	if err := instanceAnomalyRepo.Setup(); err != nil {
		logger.Error("failed to setup instance anomaly repository", "error", err)
		os.Exit(1)
	}
	if err := instanceHeartbeatRepo.Setup(); err != nil {
		// Non-fatal: this table only reports liveness to the operator console.
		// Refusing to start because that table is missing would take the control
		// plane down to protect its own monitoring.
		logger.Error("failed to setup instance heartbeat repository; the admin console cannot see backend restarts", "error", err)
	}
	if err := organizationalUnitRepo.Setup(); err != nil {
		logger.Error("failed to setup organizational unit repository", "error", err)
		os.Exit(1)
	}
	if err := accountPolicyRepo.Setup(); err != nil {
		logger.Error("failed to setup account policy repository", "error", err)
		os.Exit(1)
	}
	// Provisioning passwords: encrypt the rows written before the marker column
	// existed. Runs once the four tables are migrated, and NON-FATALLY — every
	// Setup() above exits the process, which is exactly why this cannot live in
	// one. A failure means "some rows are still plaintext", which the platform
	// serves correctly; crash-looping a live install over a hygiene backfill is
	// a failure mode worth refusing.
	for _, b := range []struct {
		what string
		run  func() (int, error)
	}{
		{"nodes", nodeRepo.BackfillDefaultPasswordEncryption},
		{"nodes unique_token", nodeRepo.BackfillUniqueTokenEncryption},
		{"organizational_units", organizationalUnitRepo.BackfillDefaultPasswordEncryption},
		{"account_policies", accountPolicyRepo.BackfillDefaultPasswordEncryption},
		{"master_tokens", masterTokenRepo.BackfillDefaultPasswordEncryption},
	} {
		if converted, err := b.run(); err != nil {
			logger.Error("failed to encrypt existing provisioning passwords; rows remain in plaintext",
				"table", b.what, "converted", converted, "error", err)
		} else if converted > 0 {
			logger.Info("encrypted existing provisioning passwords", "table", b.what, "rows", converted)
		}
	}
	if err := nodeFavoriteRepo.Setup(); err != nil {
		logger.Error("failed to setup node favorite repository", "error", err)
		os.Exit(1)
	}
	if err := nodeTombstoneRepo.Setup(); err != nil {
		logger.Error("failed to setup node tombstone repository", "error", err)
		os.Exit(1)
	}
	// AFTER that Setup, not with the block above: the backfill reads
	// unique_token_enc, and this table is migrated HERE rather than with the
	// four provisioning-password tables. Run with them, it would query a
	// column that does not exist yet on a first boot and — non-fatally — leave
	// those tokens plaintext until the next restart. Same non-fatal contract
	// as the block above.
	if converted, err := nodeTombstoneRepo.BackfillUniqueTokenEncryption(); err != nil {
		logger.Error("failed to encrypt existing device tokens; rows remain in plaintext",
			"table", "node_tombstones unique_token", "converted", converted, "error", err)
	} else if converted > 0 {
		logger.Info("encrypted existing device tokens", "table", "node_tombstones unique_token", "rows", converted)
	}
	// This table is account-scoped: any tool that deletes an account's rows
	// directly in the database must know it, and can be granted access to it
	// only after this backend has started once and AutoMigrate created it.
	if err := accountDeletionRequestRepo.Setup(); err != nil {
		logger.Error("failed to setup account deletion request repository", "error", err)
		os.Exit(1)
	}
	if err := instanceEntitlementRepo.Setup(); err != nil {
		logger.Error("failed to setup instance entitlement repository", "error", err)
		os.Exit(1)
	}
	if err := adoptionJobRepo.Setup(); err != nil {
		logger.Error("failed to setup node adoption job repository", "error", err)
		os.Exit(1)
	}
	if err := secretRepo.Setup(); err != nil {
		logger.Error("failed to setup secret repository", "error", err)
		os.Exit(1)
	}
	if err := secretAttachmentRepo.Setup(); err != nil {
		logger.Error("failed to setup secret attachment repository", "error", err)
		os.Exit(1)
	}
	if err := secretTagRepo.Setup(); err != nil {
		logger.Error("failed to setup secret tag repository", "error", err)
		os.Exit(1)
	}
	if err := accessEntryRepo.Setup(); err != nil {
		logger.Error("failed to setup access entry repository", "error", err)
		os.Exit(1)
	}
	if err := accountMemberRepo.Setup(); err != nil {
		logger.Error("failed to setup account member repository", "error", err)
		os.Exit(1)
	}
	// Must run after userRepo, accountRepo and accountMemberRepo: it
	// backfills memberships + super_admin bindings from legacy users.
	if err := roleBindingRepo.Setup(); err != nil {
		logger.Error("failed to setup role binding repository", "error", err)
		os.Exit(1)
	}
	if err := accountInviteRepo.Setup(); err != nil {
		logger.Error("failed to setup account invite repository", "error", err)
		os.Exit(1)
	}
	if err := accountRoleSettingRepo.Setup(); err != nil {
		logger.Error("failed to setup account role setting repository", "error", err)
		os.Exit(1)
	}
	if err := auditEventRepo.Setup(); err != nil {
		logger.Error("failed to setup audit event repository", "error", err)
		os.Exit(1)
	}
	if err := externalIdentityRepo.Setup(); err != nil {
		logger.Error("failed to setup external identity repository", "error", err)
		os.Exit(1)
	}
	if err := utilityAssignmentRepo.Setup(); err != nil {
		logger.Error("failed to setup utility assignment repository", "error", err)
		os.Exit(1)
	}
	if err := nodeUtilityStateRepo.Setup(); err != nil {
		logger.Error("failed to setup node utility state repository", "error", err)
		os.Exit(1)
	}
	// The row is marked migrated only now, after every Setup() above has
	// succeeded: a failed migration exits before this point and leaves the
	// row unfinished, so the next boot of this build migrates again and an
	// older build is refused. A failure to mark is fatal too, and the retry
	// is harmless, because AutoMigrate is idempotent and the row is keyed on
	// (version, revision).
	if err := markSchemaMigrated(context.Background(), schemaLedgerRepo, schema.Version, buildRevision(), logger); err != nil {
		logger.Error("failed to record the schema-version ledger", "error", err)
		os.Exit(1)
	}
	logger.Info("database migrations completed successfully")

	// One-shot key-rotation rewrap (SECRETS_ENCRYPTION_REWRAP): re-encrypt every
	// encrypted column under the current primary key, before traffic. Idempotent;
	// unset the flag after it succeeds.
	if cfg.Secrets.Rewrap {
		logger.Info("SECRETS_ENCRYPTION_REWRAP set: re-encrypting all secrets under the primary key")
		// NOT fatal: unrewrapped rows still decrypt via the old fallback key, so
		// serving is unaffected. Exiting here turned a partial rewrap into a
		// crash-loop. The operator sees the error, fixes
		// the cause, and re-runs the one-shot.
		if err := sql.RewrapEncryptedData(db, secretEncryptor); err != nil {
			logger.Error("secret rewrap did not fully complete; keep the old key in SECRETS_ENCRYPTION_KEY_OLD and re-run", "error", err)
		} else {
			logger.Info("secret rewrap completed")
		}
	}

	// Whenever an old key is still configured, report how many values actually
	// depend on it. Rotation had no hard signal before this: the ciphertext
	// prefix marks the format, not the key, so "is the rewrap complete" could
	// only be answered by trial decryption — and dropping the old key too early
	// surfaces weeks later as an unreadable secret.
	if len(cfg.Secrets.OldEncryptionKeys) > 0 {
		// FULL scan here, and only here: "can the old key be dropped" is a
		// question about every remaining value, and the row still on the old key
		// is exactly the one a sample would miss.
		coverage := sql.VerifyEncryptionKeyCoverage(db, secretEncryptor, sql.CoverageFull)
		if sql.KeyCoverageIsClean(coverage) {
			logger.Info("encryption key coverage: every value is readable by the primary key; the old key can be dropped",
				"detail", sql.FormatKeyCoverage(coverage))
		} else {
			logger.Warn("encryption key coverage: values still depend on an old key — do NOT drop SECRETS_ENCRYPTION_KEY_OLD yet",
				"detail", sql.FormatKeyCoverage(coverage))
		}
	} else {
		// Canary: is the key we hold the key this database was written with?
		//
		// Nothing asked that before. Config validation checks only that a key is
		// non-empty, and the coverage report above runs solely when the operator
		// has already declared a rotation — so a backend started against an
		// existing database with the WRONG key boots cleanly, serves, and begins
		// writing new ciphertext alongside rows nobody can ever read. The
		// scenario is not exotic: restoring a database dump onto a host whose
		// key was freshly generated instead of carried over looks exactly like
		// this. A dump is readable only with the key it was written under, and
		// this canary is what says so at boot, before new ciphertext under the
		// wrong key piles up beside it.
		//
		// A WARN, not a refusal, and the distinction is deliberate. Refusing to
		// boot would turn "wrong key" into "no control plane", and there is one
		// legitimate way to see unreadable rows with a correct key: a rotation
		// whose old key was dropped too early, where the operator needs the
		// running system to put it back. The point is that the condition becomes
		// LOUD and dated instead of silent.
		//
		// SAMPLED, unlike the rotation report above, and the two differences are
		// deliberate. A wrong key fails EVERY value it meets, so a bounded sample
		// per target proves it just as well as a full scan; and the inventory now
		// includes node_commands, which holds a payload for every command ever
		// issued and has no retention — trial-decrypting all of it on every boot
		// is a sweep that grows without bound. The condition tested is therefore
		// "unreadable by any key", not "not clean": a sampled report is
		// deliberately never clean, and reusing that predicate here would fire
		// this alarm on every boot of a healthy install.
		coverage := sql.VerifyEncryptionKeyCoverage(db, secretEncryptor, sql.CoverageSample)
		if sql.KeyCoverageHasUnreadable(coverage) {
			logger.Warn("ENCRYPTION KEY MISMATCH: this database holds values the configured SECRETS_ENCRYPTION_KEY cannot read. "+
				"If this host was restored from a backup, the original key was not restored with it — stop and put it back before writing anything. "+
				"If a rotation is in progress, set SECRETS_ENCRYPTION_KEY_OLD.",
				"detail", sql.FormatKeyCoverage(coverage))
		}
	}

	// Mark all online nodes as offline on startup
	// This ensures stale connection states are reset before the gRPC server starts accepting connections
	logger.Info("resetting node connection states")
	affectedNodes, err := nodeRepo.MarkAllOnlineNodesOffline(context.Background())
	if err != nil {
		logger.Error("failed to reset node connection states", "error", err)
		os.Exit(1)
	}
	if affectedNodes > 0 {
		logger.Info("marked online nodes as offline", "count", affectedNodes)
	}

	logger.Info("initializing services")
	// The audit recorder is composed before anything that mutates, because most
	// services now require it: a privileged action with no recorded author is the
	// gap, and a required constructor argument is what keeps a new service from
	// re-opening it.
	auditRecorder := audit_service.NewRecorder(auditEventRepo, logger)
	// The trail's read side. A separate type on purpose: only the HTTP handler
	// holds it, so none of the writing services gains a read capability.
	auditReader := audit_service.NewReader(auditEventRepo, logger)
	userService := user.NewUserService(userRepo, auditEventRepo, logger)
	accountService := account.NewAccountService(accountRepo, userRepo, accountMemberRepo, roleBindingRepo, accountInviteRepo, accountRoleSettingRepo, auditRecorder, logger)

	// Initialize WebSocket client channel
	logger.Info("initializing websocket infrastructure")
	websocketClient := websockets.NewWebsocketClientChannel()

	// Initialize gRPC server first (needed for node service)
	// Create a temporary basic node service for gRPC server initialization
	// This will be replaced with the full service later
	logger.Info("initializing gRPC server")
	// Create temporary node command service and client for gRPC server
	tempNodeCommandClient := node_commands.NewNodeCommandClientChannel()
	tempNodeCommandService := node_command_service.NewService(nodeCommandRepo, websocketClient, auditRecorder, logger)
	tempNodeService := node_service.NewService(nodeRepo, sshKeyRepo, nil, nil, tempNodeCommandService, tempNodeCommandClient, websocketClient, auditRecorder, logger)
	grpcServer := grpc.NewServer(cfg, logger, tempNodeService)
	// Set WebSocket client for status updates
	grpcServer.SetWebsocketClient(websocketClient)

	// Initialize OIDC client (optional; discovery may fail → OIDC stays off).
	var oidcClient *oidcapi.Client
	if cfg.OIDC.Enabled && cfg.OIDC.IssuerURL != "" && cfg.OIDC.ClientID != "" {
		oc, err := oidcapi.NewClient(context.Background(), cfg.OIDC.IssuerURL, cfg.OIDC.ClientID, cfg.OIDC.ClientSecret, cfg.OIDC.RedirectURL)
		if err != nil {
			logger.Error("OIDC discovery failed; OIDC login disabled", "issuer", cfg.OIDC.IssuerURL, "error", err)
		} else {
			oidcClient = oc
			logger.Info("OIDC integration enabled", "issuer", cfg.OIDC.IssuerURL, "auto_provision", cfg.OIDC.AutoProvision)
		}
	}

	// Initialize auth service
	// Second factor. Built before the auth service because the login path gates
	// on it; it depends only on repositories, so there is no cycle.
	twoFactorService := two_factor_service.NewService(twoFactorRepo, userRepo, secretEncryptor, logger)
	twoFactorPolicy := two_factor_service.NewPolicy(accountRepo, twoFactorService, logger)
	// Passkeys. The relying-party id is normally derived from the request host
	// so one binary serves whatever hostname it is deployed under; WEBAUTHN_RP_ID
	// pins it for deployments behind a proxy that rewrites Host.
	passkeyService := passkey_service.NewService(passkeyRepo, userRepo, cfg.Server.WebAuthnRPID, logger)
	// A passkey already satisfies the account's two-factor requirement.
	twoFactorPolicy.SetPasskeyCounter(passkeyRepo)
	// Password users only: a federated member with no password is exempt from
	// the enrolment mandate (see Policy.UserHasTwoFactor).
	twoFactorPolicy.SetPasswordLookup(userRepo)
	// A linked IdP account is also a way to sign in, so deleting the last
	// passkey of a passwordless-but-federated user is safe and must not be
	// refused. Without this the guard is merely stricter, never wrong.
	passkeyService.SetIdentityLister(externalIdentityRepo)

	// Step-up confirmation in front of credential changes. Depends only on
	// repositories and the two-factor service, so it slots in before auth.
	reauthService := reauth_service.NewService(
		userRepo,
		passkeyRepo,
		twoFactorService,
		services.NewBCryptPasswordHasher(0),
		logger,
	)

	authService := auth.NewAuthService(
		cfg.JWT,
		cfg.Server.InviteCode,
		cfg.Server.MaxAccounts,
		auth.OpenRegistration{
			Enabled:       cfg.Server.OpenRegistration,
			MaxPending:    cfg.Server.OpenRegistrationMaxPending,
			PendingWindow: cfg.Server.OpenRegistrationPendingWindow(),
		},
		userRepo,
		accountRepo,
		accountMemberRepo,
		roleBindingRepo,
		accountInviteRepo,
		externalIdentityRepo,
		refreshTokenRepo,
		twoFactorService,
		// The unlink last-credential guard counts passkeys, so the auth service
		// needs to see them.
		passkeyRepo,
		accountService,
		logger,
	)
	// Bound concurrent sessions per user and report runaway session creation.
	// Both are set after construction so a nil anomaly feed stays a supported
	// configuration (an install without the console has nothing to read it).
	// One shared per-user credential lock across the two services that can each
	// remove a sign-in method, so their last-credential guards serialize
	// against each other. Same instance to both, or they do not.
	credentialLock := credlock.New()
	passkeyService.SetCredentialLock(credentialLock)
	authService.SetCredentialLock(credentialLock)

	authService.SetSessionCap(cfg.JWT.MaxSessionsPerUser)
	authService.SetAnomalyRecorder(instanceAnomalyRepo)
	// The step-up guard needs to know a federated user CAN prove themselves, at
	// their identity provider. Without this they read as "no credential at all"
	// and were waved through the credential-change guard.
	reauthService.SetIdentityCounter(authService)
	// A step-up ticket is issued only to a LIVE session and honoured only while
	// it stays one; without this the guard refuses everything.
	reauthService.SetSessionVerifier(authService)

	// Email verification: proves mailbox ownership, which is what allows a
	// later Google login to attach to a password account silently. Delivery is
	// optional — with no SMTP configured the service is inert and users simply
	// stay unverified.
	var emailSender email_verification_service.Sender
	if cfg.Email.Configured() {
		emailSender = mailer.NewSMTPMailer(cfg.Email, logger)
		logger.Info("email delivery enabled", "smtp_host", cfg.Email.Host, "from", cfg.Email.FromAddress)
	} else if cfg.Email.Enabled {
		logger.Warn("email is enabled but incompletely configured (need smtp_host and from_address); verification links will not be sent")
	}
	emailVerificationService := email_verification_service.NewService(
		emailVerificationRepo, userRepo, emailSender, cfg.Server.AppBaseURL, logger)
	// Wired via setters: both services need the verifier, and the verifier
	// needs the user repository they are built from.
	authService.SetEmailVerifier(emailVerificationService)
	userService.SetEmailVerifier(emailVerificationService)
	// A sign-up that carries a terms-of-use acceptance is recorded through the
	// same acknowledgement the user service exposes.
	authService.SetTermsRecorder(userService)
	// The OLD address gets a plain notice when the email changes — it is the
	// mailbox an evicted owner still reads. Rides the same transport directly
	// (a notice needs no token, so the verification service adds nothing).
	userService.SetChangeNotifier(emailSender)

	// Self-service password reset rides on the same transport. Without SMTP it
	// is inert, and operators reset passwords from the host with
	// --reset-password (reset_credentials.go) or from the admin console.
	passwordResetService := password_reset_service.NewService(
		passwordResetRepo, userRepo, emailSender, cfg.Server.AppBaseURL, logger)

	// Initialize HTTP server with the websocket client's channel
	logger.Info("initializing HTTP server")
	server := http_server.NewServer(authService, websocketClient.GetChannel(), logger, cfg)

	// Create main context that will be canceled on shutdown
	mainCtx, mainCancel := context.WithCancel(context.Background())
	defer mainCancel()

	// If the operator moved the temp directory off tmpfs, make sure it exists.
	//
	// It has to: Go's multipart parser spills anything past its 32 MiB
	// in-memory budget to os.TempDir(), and in a container /tmp is tmpfs — RAM
	// charged to the memory limit — so a multi-gigabyte image upload can
	// OOM-kill the backend. TMPDIR moves that spill onto the storage
	// volume, but a TMPDIR that does not exist turns the OOM into "every upload
	// fails", which is not an improvement. Non-fatal: if it cannot be created,
	// say so and let Go fall back to /tmp rather than refusing to start.
	if tmpDir := os.Getenv("TMPDIR"); tmpDir != "" {
		if err := os.MkdirAll(tmpDir, 0o700); err != nil {
			logger.Warn("TMPDIR is set but could not be created; large uploads will spill to the default temp dir",
				"tmpdir", tmpDir, "error", err)
		} else {
			logger.Info("upload spill directory ready", "tmpdir", tmpDir)
		}
	}

	var storageClient storage.StorageClient
	// Set only on the local backend: object storage has no partial-store class
	// (a failed PUT leaves nothing behind), so there is nothing to sweep there.
	var localTempSweep func(context.Context)
	// One decision shared with --import-agent-release (storage_backend.go).
	backendKind := storageBackendKind(cfg)
	useGCP := backendKind == storage.TypeGCP
	useS3 := backendKind == storage.TypeS3

	var s3Client *s3_storage.S3StorageClient

	if useS3 {
		var err error
		s3Client, err = s3_storage.NewS3StorageClient(s3ConfigFrom(cfg), logger)
		if err != nil {
			// Misconfiguration, not an outage: the constructor does no I/O, so
			// the only way it fails is a missing bucket/endpoint/credential.
			// Exiting is right — the alternative is a control plane that
			// silently stores images somewhere else.
			logger.Error("failed to create S3 storage client", "error", err)
			os.Exit(1)
		}
		// Reachability is checked, but NOT fatally. A bucket that is briefly
		// unreachable at boot must not become a backend that refuses to start:
		// nodes would lose their control plane over a storage outage that only
		// affects images. /ready reports it instead, which is what an external
		// probe already watches.
		checkCtx, cancelCheck := context.WithTimeout(mainCtx, 10*time.Second)
		if err := s3Client.CheckAccess(checkCtx); err != nil {
			logger.Error("S3 bucket is not reachable at startup; image storage will fail until it is",
				"error", err, "bucket", cfg.Storage.S3.Bucket, "endpoint", cfg.Storage.S3.Endpoint)
		} else {
			logger.Info("S3 storage client initialized",
				"bucket", cfg.Storage.S3.Bucket, "endpoint", cfg.Storage.S3.Endpoint, "region", cfg.Storage.S3.Region)
		}
		cancelCheck()
		storageClient = s3Client
	} else if useGCP {
		var gcpClient *gcp_storage.GCPStorageClient
		var err error

		if cfg.Storage.GCP.Credentials != "" {
			credentialsJSON, err := base64.StdEncoding.DecodeString(cfg.Storage.GCP.Credentials)
			if err != nil {
				logger.Error("failed to decode base64 credentials", "error", err)
				os.Exit(1)
			}
			gcpClient, err = gcp_storage.NewGCPStorageClientWithCredentials(mainCtx, cfg.Storage.GCP.BucketName, cfg.Storage.GCP.ProjectID, credentialsJSON)
		} else if cfg.Storage.GCP.CredentialsFile != "" {
			gcpClient, err = gcp_storage.NewGCPStorageClient(mainCtx, cfg.Storage.GCP.BucketName, cfg.Storage.GCP.ProjectID, cfg.Storage.GCP.CredentialsFile)
		} else {
			gcpClient, err = gcp_storage.NewGCPStorageClient(mainCtx, cfg.Storage.GCP.BucketName, cfg.Storage.GCP.ProjectID, "")
		}

		if err != nil {
			logger.Error("failed to create GCP storage client", "error", err)
			os.Exit(1)
		}
		storageClient = gcpClient
		logger.Info("GCP storage client initialized", "bucket", cfg.Storage.GCP.BucketName, "project", cfg.Storage.GCP.ProjectID)

		defer func() {
			if err := gcpClient.Close(); err != nil {
				logger.Error("failed to close GCP storage client", "error", err)
			}
		}()
	} else {
		localClient, err := local_storage.NewLocalStorageClient(cfg.Storage.Local.BaseDir, cfg.Storage.Local.BaseURL)
		if err != nil {
			logger.Error("failed to create local storage client", "error", err)
			os.Exit(1)
		}
		storageClient = localClient
		// Abandoned partial stores. A store that dies between the temp write and
		// the rename leaves a multi-GB file under a RANDOM name, so it is on no
		// row: the byte quota, an out-of-band account purge and account deletion all miss it,
		// and it sits on the volume that holds every image. Same age bound as the
		// multipart spill sweep — both answer "the process that owned this is
		// gone".
		localTempSweep = func(ctx context.Context) {
			localClient.StartTempSweep(ctx, time.Duration(cfg.Storage.TmpSweepHours)*time.Hour, logger)
		}
		logger.Info("Local storage client initialized", "base_dir", cfg.Storage.Local.BaseDir, "base_url", cfg.Storage.Local.BaseURL)
	}

	// Readiness, wired here rather than in NewServer because the filesystem it
	// has to watch is only known once the storage backend is chosen. On object
	// storage there is no local path to statfs, so the storage check is left
	// out of the report entirely instead of passing vacuously.
	readinessStoragePath := ""
	if !useGCP && !useS3 {
		readinessStoragePath = cfg.Storage.Local.BaseDir
	}
	readinessChecker := health.NewGormChecker(db, readinessStoragePath, logger)
	if s3Client != nil {
		// Replaces the free-space check rather than adding to it. On object
		// storage the local disk says nothing about whether an image can be
		// stored, and "can I reach the bucket" is the question a readiness
		// probe should be answering.
		readinessChecker.SetObjectStorageChecker(s3Client.CheckAccess)
	}
	server.RegisterReadiness(readinessChecker)

	var imageService services.ImageService
	if storageClient != nil {
		concreteImageService := image_service.NewServiceWithWebSocket(imageRepo, storageClient, cfg, websocketClient)
		// The deploy format is multi-block xz (`xz -T0`): measured install
		// speed equals zstd (the pipeline is network/disk-bound, 26s for a
		// 4.9GB image either way) while the archive is ~8% smaller and equal
		// in size to official single-block .xz uploads. zstd remains as a
		// fallback compressor and as the decompressor for .zst uploads.
		if cfg.Image.XZMemLimitClamped() {
			logger.Warn("IMAGE_XZ_MEMLIMIT_MB raised to the multi-block floor; below it xz emits single-block archives",
				"configured_mb", cfg.Image.XZMemLimitMB, "effective_mb", cfg.Image.EffectiveXZMemLimitMB())
		}
		if cfg.Image.CompressionMemoryBudgetClamped() {
			logger.Warn("IMAGE_COMPRESSION_MEMORY_BUDGET_MB raised to two process limits; a transcode is a decoder and an encoder and must fit",
				"configured_mb", cfg.Image.CompressionMemoryBudgetMB, "effective_mb", cfg.Image.EffectiveCompressionMemoryBudgetMB())
		}
		// Read from the cgroup, not assumed: the budget is only a bound the
		// process can survive if it fits the container limit with room for the
		// Go process itself.
		if limitMB := config.CgroupMemoryLimitMB(); limitMB > 0 {
			const goHeadroomMB = config.CompressionGoHeadroomMB
			budget := cfg.Image.EffectiveCompressionMemoryBudgetMB()
			if budget+goHeadroomMB > limitMB {
				logger.Warn("compression memory budget leaves no headroom inside the container's memory limit; a transcode at its ceiling can OOM-kill the backend",
					"budget_mb", budget, "cgroup_limit_mb", limitMB, "headroom_mb", goHeadroomMB,
					"remedy", "lower IMAGE_XZ_MEMLIMIT_MB or IMAGE_COMPRESSION_MEMORY_BUDGET_MB, or raise the container limit")
			} else {
				logger.Info("compression memory budget fits the container", "budget_mb", budget, "cgroup_limit_mb", limitMB)
			}
		}
		xzTool, xzErr := compression_infra.NewXZCLICompressorWithMemLimit(cfg.Image.EffectiveXZMemLimitMB())
		zstdTool, zstdErr := compression_infra.NewZstdCLICompressor()
		switch {
		case xzErr == nil:
			concreteImageService.SetCompressor(xzTool, logger)
			if zstdErr == nil {
				concreteImageService.SetDecompressors(xzTool, zstdTool)
			} else {
				logger.Warn("zstd binary not available - .zst transcoding disabled", "error", zstdErr)
				concreteImageService.SetDecompressors(xzTool)
			}
		case zstdErr == nil:
			logger.Warn("xz not available, using zstd for image compression; the compression memory budget charges it as one xz process limit per job, which bounds its thread count (-T2) but is not a measured ceiling for zstd — install xz or size the container for the fallback",
				"error", xzErr, "budget_mb", cfg.Image.EffectiveCompressionMemoryBudgetMB())
			concreteImageService.SetCompressor(zstdTool, logger)
			concreteImageService.SetDecompressors(zstdTool)
		default:
			logger.Warn("no compression binary available - server-side image compression disabled", "xz_error", xzErr, "zstd_error", zstdErr)
		}
		// Before the statuses are reset below — the in-progress status is the
		// only thing identifying which records could own a temporary object, so
		// once it is cleared the orphan cannot be found again.
		if err := concreteImageService.SweepStaleCompressionArtifacts(context.Background()); err != nil {
			logger.Error("failed to sweep stale compression artifacts", "error", err)
		}
		imageService = concreteImageService
	} else {
		// Create service without storage (upload will fail, but other operations work)
		// In production, storage should always be configured
		logger.Warn("image service initialized without storage client - file uploads will not work")
		imageService = image_service.NewServiceWithWebSocket(imageRepo, nil, cfg, websocketClient)
	}

	// Compression goroutines do not survive restarts; mark stale jobs failed.
	if err := imageRepo.ResetInProgressCompression(context.Background()); err != nil {
		logger.Error("failed to reset stale image compression states", "error", err)
	}

	// Create node file service
	nodeFileService := node_file_service.NewService(nodeFileRepo, logger)
	// Tracked-file quotas. Configurable per install (0 = unlimited), defaulting
	// to the values the feature shipped with rather than to unlimited: unbounded
	// file history was never a chosen behaviour, only an unconfigurable one.
	nodeFileLimits := node_file_domain.Limits{
		VersionsPerPath: cfg.NodeFile.MaxVersionsPerPath,
		FilesPerNode:    cfg.NodeFile.MaxFilesPerNode,
		BytesPerNode:    cfg.NodeFile.MaxBytesPerNode,
		BytesPerAccount: cfg.NodeFile.MaxBytesPerAccount,
	}
	nodeFileService.SetLimits(nodeFileLimits)
	// Set node file service on gRPC server
	grpcServer.SetNodeFileService(nodeFileService)

	// A peer that fails the handshake has no node and no account, so the gRPC
	// layer is the only place that can report it at all. Without this the
	// condition is log-only, and a looping device drowns the log it appears in.
	grpcServer.SetAnomalyRepository(instanceAnomalyRepo)

	// Create node command client (queue)
	nodeCommandClient := node_commands.NewNodeCommandClientChannel()
	commandQueue := nodeCommandClient.GetChannel()

	// Create node command service
	nodeCommandService := node_command_service.NewService(nodeCommandRepo, websocketClient, auditRecorder, logger)

	// Create node service with all dependencies
	nodeService := node_service.NewService(nodeRepo, sshKeyRepo, grpcServer, nodeFileService, nodeCommandService, nodeCommandClient, websocketClient, auditRecorder, logger)
	nodeService.SetTrackedFileRepository(nodeTrackedFileRepo)
	nodeService.SetNodeFileLimits(nodeFileLimits)
	// Per-account quotas. Wired as setters here rather than
	// through each constructor: they are optional by design (0 = unlimited) and
	// three of these constructors already take nine arguments.
	nodeService.SetMaxNodesPerAccount(cfg.Quota.MaxNodesPerAccount)
	nodeService.SetBootHost(cfg.Provisioning.BootHost)
	// The device entitlement: the instance-wide cap every admission path
	// checks. Resolved from the boot file (ENTITLEMENT / ENTITLEMENT_FILE) and
	// the files applied through the API; with neither, the free cap applies.
	// There is deliberately no build flag and no environment exemption: the
	// hosted install runs this same code under its own file.
	entitlementService := entitlement_service.NewService(instanceEntitlementRepo, nodeRepo, accountRepo,
		cfg.Entitlement.Document, cfg.Entitlement.SubscriptionURL, auditRecorder, logger)
	// The per-account device limit (ACCOUNT_DEVICE_LIMIT, 0 = off) is
	// answered by the same service, so the admission, the subscription card
	// and the Status page read one rule. The override columns are read only
	// while it is on.
	entitlementService.SetAccountDeviceLimit(cfg.Entitlement.EffectiveAccountDeviceLimit(),
		cfg.Entitlement.EffectiveAccountDeviceLimitContact(), accountRepo)
	entitlementService.LogBootState(context.Background())
	nodeService.SetDeviceCapResolver(entitlementService)
	nodeService.SetAccountDeviceLimitResolver(entitlementService)
	// The refusal at the cap asks the same resolver whether the caller
	// operates the install, so a tenant is never shown the install-wide
	// count. Optional on the interface (a resolver without it yields the
	// tenant view), so the compiler is told here that this one has it.
	var _ node_service.InstanceOperatorResolver = entitlementService
	nodeService.SetAdoptionCodeRepo(adoptionCodeRepo)
	// Per-user favorite marks are keyed on node_id alone, so nothing else can
	// clean them up when the node goes.
	nodeService.SetNodeFavoriteRepo(nodeFavoriteRepo)
	// Node-scoped secret attachments and access entries key on node_id alone and
	// nothing else cleans them up. Node ids are reused (a tombstone restore
	// reuses the original), so leaving them re-delivers a deleted node's
	// credentials to whatever answers to that id next.
	nodeService.SetSecretAttachmentRepo(secretAttachmentRepo)
	// Attribute-backing secrets (attr:<nodeID>:<key>) exist only as one node's
	// attributes; nothing else deletes them, and each orphan counts against
	// SECRET_MAX_PER_ACCOUNT forever.
	nodeService.SetSecretRepo(secretRepo)
	nodeService.SetAccessEntryRepo(accessEntryRepo)
	// Deleted-node records. Without this a delete removes the row and tells the
	// device nothing: it retries forever with an identity nothing recognises, and
	// there is no route back short of physical access.
	nodeService.SetNodeTombstoneRepo(nodeTombstoneRepo)
	// A restore puts the node back into its recorded unit under the same lock an
	// ordinary assignment takes, and drops a unit deleted in the meantime.
	nodeService.SetUnitAssigner(ouMutationRepo)
	// capture_image creates a pending image record at command-persist time.
	nodeService.SetImageService(imageService)
	// Operator-facing signals (cross-account serial conflicts), for an operator
	// console outside this tree — a refused adoption leaves no other trace in
	// the database.
	nodeService.SetAnomalyRepo(instanceAnomalyRepo)
	// Set node service on node command service (after both are created to avoid circular dependency)
	nodeCommandService.SetNodeService(nodeService)
	// Wire the dispatch channel and agent-version lookup so commands queued for
	// an offline node can be redelivered on reconnect, re-checking the version
	// floor (a node can come back on an older agent than it left with).
	nodeCommandService.SetCommandDispatcher(nodeCommandClient, nodeService)
	// A capture_image that ends without streaming fails its image record with
	// it, on every route that settles the command.
	nodeCommandService.SetCaptureSettler(imageService)
	// Set node service on gRPC server for handling storage device updates
	grpcServer.SetNodeService(nodeService)
	// Set node command service on gRPC server
	grpcServer.SetNodeCommandService(nodeCommandService)
	// Set image service on gRPC server for install_os command
	grpcServer.SetImageService(imageService)

	// Create SSH key service
	sshKeyService := ssh_key_service.NewService(sshKeyRepo, auditRecorder, logger)

	// Create master token service
	masterTokenService := master_token_service.NewService(masterTokenRepo, sshKeyRepo, organizationalUnitRepo, auditRecorder, logger)
	// Set master token service on gRPC server for master token authentication
	grpcServer.SetMasterTokenService(masterTokenService)

	// Create organizational unit service (OU tree + node membership/policy)
	organizationalUnitService := organizational_unit_service.NewService(organizationalUnitRepo, ouMutationRepo, nodeRepo, sshKeyRepo, secretRepo, secretAttachmentRepo, accountPolicyRepo, imageRepo, roleBindingRepo, accountInviteRepo, masterTokenRepo, workflowTemplateRepo, auditRecorder, logger)
	// gRPC needs it to fill install_os/provision fields from the effective policy
	grpcServer.SetOrganizationalUnitService(organizationalUnitService)

	// Load the device-profile registry (embedded YAML). Fatal on error in the
	// composition root: a registry the binary cannot parse or validate must
	// not serve traffic — the same files are validated in go test, so this
	// only fires on a build that skipped CI.
	deviceProfileCatalog, err := device_profile_registry.Load()
	if err != nil {
		logger.Error("failed to load device profile registry", "error", err)
		os.Exit(1)
	}
	organizationalUnitService.SetDeviceProfileCatalog(deviceProfileCatalog)
	nodeService.SetDeviceProfileCatalog(deviceProfileCatalog)
	nodeService.SetDeviceProfileResolver(organizationalUnitService)
	organizationalUnitService.SetNodeChangeNotifier(nodeService)

	// Create access service (declarative shell access: named users + key sets +
	// CA trust). Wire its repo into the OU-delete and key-delete cascades.
	accessService := access_service.NewService(accessEntryRepo, sshKeyRepo, organizationalUnitRepo, nodeRepo, auditRecorder, logger)
	organizationalUnitService.SetAccessEntryRepo(accessEntryRepo)
	sshKeyService.SetAccessEntryRepo(accessEntryRepo)
	// Auto-propagate access changes to devices: an access mutation enqueues a
	// (superseding, queueable) apply_access_config for the affected nodes, and a
	// key removed from the inventory pushes the resulting revocation fleet-wide.
	accessService.SetApplier(nodeService)
	sshKeyService.SetAccessPropagator(accessService)
	// The OU service changes a node's effective access without touching an
	// access entry at all — moving the node between units swaps every OU-level
	// user it has, and ssh_password_auth is applied by the same command — so it
	// needs the same dispatcher.
	organizationalUnitService.SetAccessApplier(nodeService)
	// gRPC fills the node's effective access set when building apply_access_config.
	grpcServer.SetAccessService(accessService)
	// The node service resolves the same desired hash to compare against the
	// generation the device confirmed, so a managed-OS connect re-applies a
	// config that drifted while the node was away.
	nodeService.SetAccessGenerationResolvers(organizationalUnitService, accessService)

	// Load the utility registry (embedded YAML) — same contract as the
	// device-profile registry: fatal on error in the composition root.
	utilityCatalog, err := utility_registry.Load()
	if err != nil {
		logger.Error("failed to load utility registry", "error", err)
		os.Exit(1)
	}
	// Utilities (OU-assignable software roles, docs/UTILITIES-PLAN.md).
	utilityService := utility_service.NewService(
		utilityCatalog, utilityAssignmentRepo, nodeUtilityStateRepo,
		organizationalUnitRepo, nodeRepo, auditRecorder, logger,
		cfg.Quota.MaxUtilityAssignmentsPerAccount, cfg.Quota.MaxConcurrentUtilityApplies)
	utilityService.SetDispatcher(nodeService)
	utilityService.SetActiveApplyCounter(nodeCommandRepo)
	nodeService.SetUtilityStateCleaner(utilityService)
	nodeService.SetUtilityReconciler(utilityService)
	// The account's identify blink duration (accounts.identify_duration_sec),
	// fail-soft to the 300 s default.
	nodeService.SetIdentifyDurationSource(func(ctx context.Context, accountID uuid.UUID) int {
		acc, err := accountRepo.GetByID(ctx, accountID)
		if err != nil || acc == nil {
			return 0
		}
		return acc.IdentifyDurationSec
	})
	// {{secrets.*}}/{{node.attributes.*}} in utility params resolve through
	// the same interface the workflow engine uses.
	utilityService.SetSecretsResolver(organizationalUnitService)
	// The gRPC terminal-status hook settles the per-utility ladder from the
	// command's own payload hashes.
	grpcServer.SetUtilitySettler(utilityService)

	// Create secret service (account-scoped encrypted secrets)
	secretService := secret_service.NewService(secretRepo, secretAttachmentRepo, nodeRepo, organizationalUnitRepo, auditRecorder, logger)
	secretService.SetMaxSecretsPerAccount(cfg.Quota.MaxSecretsPerAccount)
	// Create secret tag service (tag color overrides + rename/delete lifecycle)
	secretTagService := secret_tag_service.NewService(secretTagRepo, logger)

	// Create per-user node favorites service
	nodeFavoriteService := node_favorite_service.NewService(nodeFavoriteRepo, nodeRepo, logger)

	// Create workflow services
	workflowTemplateService := workflow_template_service.NewServiceWithWebSocket(workflowTemplateRepo, organizationalUnitRepo, imageRepo, secretRepo, websocketClient, auditRecorder, logger)
	workflowExecutionService := workflow_execution_service.NewService(
		workflowExecutionRepo,
		workflowExecutionNodeRepo,
		workflowTemplateRepo,
		nodeRepo,
		nodeCommandRepo,
		organizationalUnitRepo,
		nodeService,
		nodeCommandService,
		websocketClient,
		cfg.Workflow,
		logger,
	)
	workflowExecutionService.SetAuditRecorder(auditRecorder)
	// Set workflow execution service on node command service (after both are created to avoid circular dependency)
	nodeCommandService.SetWorkflowExecutionService(workflowExecutionService)
	// Command frames follow the REST command reads' omission of run commands.
	nodeCommandService.SetRunCommandFilter(workflowExecutionService)
	// Node replacement orchestrates attribute/hostname/OU transfer + optional playbook
	nodeReplaceService := node_replace_service.NewService(nodeRepo, secretService, organizationalUnitService, workflowExecutionService, logger)
	memberService := member_service.NewMemberService(userRepo, accountRepo, accountMemberRepo, roleBindingRepo, memberMutationRepo, accountInviteRepo, organizationalUnitRepo, auditEventRepo, authService, cfg.Member, logger)
	memberService.SetMaxMembersPerAccount(cfg.Quota.MaxMembersPerAccount)
	// An invite accepted through a federated sign-in goes through the same
	// acceptance as the invite link: seat cap, OU check, audit event.
	authService.SetInviteAcceptor(memberService)
	authzEnforcer := authz_service.NewEnforcer(accountMemberRepo, roleBindingRepo, accountRoleSettingRepo, organizationalUnitRepo, auditRecorder, logger)
	// The member service invalidates the authz cache on binding/status changes
	// so revocations take effect immediately rather than after the cache TTL.
	accountService.SetTwoFactorPolicy(twoFactorPolicy)
	memberService.SetAuthzInvalidator(authzEnforcer)
	// Revoking a membership also drops the member's live sockets: a WebSocket
	// authorizes once, so nothing else would re-check them until the token expired.
	memberService.SetSessionTerminator(server.WebSocketManager())
	// Membership at authorization and OU scope on every pushed event. Without
	// the first a removed member reconnected with their still-valid access
	// token; without the second the push channel ignored the OU boundaries the
	// HTTP surface enforces, so an OU-scoped viewer passively received node
	// state for units they cannot list. The manager fails closed without it.
	server.WebSocketManager().SetMemberAuthorizer(authzEnforcer)
	// A completed password reset must actually end existing sessions, not just
	// block future refreshes.
	passwordResetService.SetSessionRevoker(authService)

	// Invite links are mailed when a transport exists; the one-shot token is
	// still returned either way, so an install without SMTP is unchanged.
	memberService.SetInviteMailer(invite_delivery_service.NewService(
		accountRepo, userRepo, emailSender, cfg.Server.AppBaseURL, logger))
	// OU deletion revokes OU-scoped bindings — drop the affected members' cache too.
	organizationalUnitService.SetAuthzInvalidator(authzEnforcer)
	// Moving a node between units repoints its live stream's captured unit, so
	// the OU-scoped metrics push follows the node without a reconnect.
	organizationalUnitService.SetNodeScopeUpdater(grpcServer)
	// Wire the control-flow engine's server-block dependencies (secrets context
	// + secret upsert) for condition/http_request/secret.upsert blocks.
	workflowExecutionService.SetControlDeps(organizationalUnitService, secretService, organizationalUnitService)
	// The cron scheduler re-checks a privileged playbook's author before every
	// unattended fire, so a since-demoted admin's schedule stops on its own.
	workflowExecutionService.SetAuthzChecker(authzEnforcer)
	nodeFavoriteService.SetReadAuthorizer(authzEnforcer)
	workflowExecutionService.SetHeartbeatRepo(instanceHeartbeatRepo)
	// Scheduler skips are otherwise invisible: a playbook that was due and did
	// not run leaves no execution, no command and no audit event.
	workflowExecutionService.SetAnomalyRepo(instanceAnomalyRepo)
	// Deleting a node must settle the runs targeting it, or they stay active
	// forever and consume the per-account cap. Wired as a setter
	// because the execution service already depends on the node service.
	nodeService.SetExecutionAbandoner(workflowExecutionService)

	// The one EEPROM signer client. It was built twice — here for SSH adopt and
	// again inside SetupRoutes — until the bootloader freshness cache needed a
	// third; the composition root owns it now and hands it out.
	var eepromSignerClient *eepromsigner.Client
	if cfg.EEPROMSigner.URL != "" {
		eepromSignerClient = eepromsigner.NewClient(cfg.EEPROMSigner.URL, cfg.EEPROMSigner.Token)
	}
	// One firmware-base cache, shared by the node sidebar's route and the fleet
	// attention check. Separate caches with separate TTLs would let the same
	// install call a node's bootloader current in one view and outdated in the
	// other for minutes at a time.
	eepromBaseCache := eepromsigner.NewBaseCache(eepromSignerClient)

	// SSH device-adopt runner (outbound SSH → install agent → wait for connect →
	// optional network-first EEPROM). Uses its own eeprom_provision instance
	// (stateless) so it doesn't depend on the node handler's copy.
	adoptEEPROM := eeprom_provision.NewService(eepromSignerClient, organizationalUnitService, nodeService, cdnsign.DeriveKey(cfg.CDNSigningSecret()), logger)
	adoptGRPCEndpoint := cfg.GRPC.AdvertisementURL
	if adoptGRPCEndpoint == "" {
		adoptGRPCEndpoint = fmt.Sprintf("%s:%d", cfg.GRPC.Host, cfg.GRPC.Port)
	}
	sshAdoptService := node_ssh_adopt_service.NewService(
		ssh_adapter.NewClient(), sshKeyService, nodeService, adoptEEPROM, grpcServer,
		adoptionJobRepo, websocketClient, auditRecorder, cfg.SSHAdopt, cfg.Server.AdvertisementURL, adoptGRPCEndpoint, logger,
	)
	if err := sshAdoptService.ResetStranded(context.Background()); err != nil {
		logger.Error("failed to reset stranded adoption jobs", "error", err)
	}

	// The Status section's two account-wide reads. Both are views over nodes,
	// so both are gated on node.read inside their handler and neither adds a
	// permission. nodeAttentionService resolves an account in one pass; the
	// activity reader is read-only by construction, so no writing service
	// gains the ability to list the account's whole command history.
	nodeAttentionService := node_attention_service.NewService(
		nodeRepo, organizationalUnitRepo, nodeCommandRepo, nodeUtilityStateRepo,
		agentrelease.NewSource(storageClient, cfg.Storage.AgentCDNBasePath),
		eepromBaseCache, logger,
	)
	nodeAttentionService.SetDeviceCapSource(entitlementService)
	nodeAttentionService.SetAccountDeviceLimitSource(entitlementService)
	// Optional on the source, asserted here: without it every caller gets the
	// tenant view of device_cap_reached, the operator included.
	var _ node_attention_service.InstanceOperatorSource = entitlementService
	nodeActivityReader := node_activity_service.NewReader(nodeCommandRepo, logger)

	http_server.SetupRoutes(server.Echo(), cfg, authService, userService, accountService, nodeService, imageService, nodeFileService, nodeCommandService, sshKeyService, masterTokenService, organizationalUnitService, accessService, utilityService, nodeFavoriteService, secretService, secretTagService, nodeReplaceService, memberService, authzEnforcer, oidcClient, storageClient, grpcServer, server.WebSocketManager(), workflowTemplateService, workflowExecutionService, sshAdoptService, emailVerificationService, passwordResetService, twoFactorService, twoFactorPolicy, passkeyService, reauthService, auditRecorder, auditReader, accountDeletionRequestRepo, eepromSignerClient, eepromBaseCache, nodeAttentionService, nodeActivityReader, entitlementService, logger)

	// Start HTTP server in a goroutine
	go func() {
		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
		logger.Info("HTTP server starting", "address", addr)

		if err := server.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Report liveness for an operator console outside this tree, which cannot
	// ask this process anything, so without this row a backend restarting
	// every ninety seconds looks perfectly healthy from outside: nodes
	// reconnect, the disk is fine, the certificate is valid.
	startHeartbeat(mainCtx, instanceHeartbeatRepo, logger)

	// Start workflow execution service worker pool + scheduler + retention
	workflowExecutionService.Start(mainCtx)
	workflowExecutionService.StartScheduler(mainCtx)
	workflowExecutionService.StartRetention(mainCtx)
	// The two tables nothing else prunes: device command history (only ever
	// deleted via its workflow execution, so everything issued outside a
	// playbook accumulated forever with its encrypted payload) and the audit
	// trail, per account. Commands default to unlimited, the trail to a year —
	// see RetentionConfig.
	retention_service.NewService(retention_service.Config{
		NodeCommandDays: cfg.Retention.NodeCommandDays,
		AuditEventDays:  cfg.Retention.AuditEventDays,
	}, nodeCommandRepo, auditEventRepo, logger).Start(mainCtx)
	// Expired refresh tokens and email verifications: both DeleteExpired
	// implementations existed with no periodic caller, so the tables grew
	// forever.
	startExpiredCredentialSweep(mainCtx, refreshTokenRepo, emailVerificationRepo, logger)
	// A hardware serial bound to a node nobody has ever admitted — a WebUSB
	// placeholder, a pending master-token adopt — is a lease, not a freehold:
	// past QuotaConfig.SerialLeaseDays with no sign of the device, the
	// fingerprint is released so the account that holds the hardware can bind
	// it. Every per-device secret is minted by the binding, so this is the one
	// cost that can be put on binding a serial one does not own.
	startSerialLeaseSweep(mainCtx, cfg.Quota.SerialLeaseDays, nodeService, logger)
	// Abandoned multipart spills. When TMPDIR points at the storage volume an
	// orphan costs image capacity and rides along into any off-host mirror of
	// that volume, and a single abandoned upload is gigabytes.
	tmpsweep.New(
		os.TempDir(),
		time.Duration(cfg.Storage.TmpSweepHours)*time.Hour,
		logger,
	).Start(mainCtx)
	// And the same class one directory over: a partial store under the storage
	// base, left by a process that died between the temp write and the rename.
	if localTempSweep != nil {
		localTempSweep(mainCtx)
	}
	// Retire queued commands past their TTL. Redelivery expires them when a node
	// reconnects; this covers the node that never does, whose command would
	// otherwise sit in "registered" forever and read as still pending.
	nodeCommandService.StartQueueExpiry(mainCtx)

	// Start gRPC server in a goroutine
	go func() {
		if err := grpcServer.Start(mainCtx, commandQueue); err != nil {
			logger.Error("gRPC server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal to gracefully shut down the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down servers...")

	// Cancel main context to signal all services to stop
	mainCancel()

	// Create a deadline for server shutdown
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Attempt graceful shutdown of servers
	if err := server.Stop(ctx); err != nil {
		logger.Error("HTTP server shutdown failed", "error", err)
	}

	if err := grpcServer.Stop(ctx); err != nil {
		logger.Error("gRPC server shutdown failed", "error", err)
	}

	logger.Info("servers stopped successfully")
}

func loadConfig(logger *slog.Logger) (*config.Config, error) {
	// Try to find the config file
	configFilePath := configPath
	if envPath := os.Getenv("CONFIG_PATH"); envPath != "" {
		configFilePath = envPath
	}

	// Ensure the path is absolute
	if !filepath.IsAbs(configFilePath) {
		workDir, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get working directory: %w", err)
		}
		configFilePath = filepath.Join(workDir, configFilePath)
	}

	logger.Info("loading configuration file", "path", configFilePath)

	// Open the config file
	var cfg *config.Config
	file, err := os.Open(configFilePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to open config file: %w", err)
		}
		logger.Warn("configuration file not found, using environment variables", "path", configFilePath)
		if cfg, err = config.Load(); err != nil {
			return nil, err
		}
	} else {
		defer file.Close()

		// Load the configuration from the file
		if cfg, err = config.LoadConfig(file); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}

		// Override with environment variables
		if cfg, err = config.ApplyEnvironment(cfg); err != nil {
			return nil, fmt.Errorf("failed to apply environment variables: %w", err)
		}
	}

	// Fail closed on insecure configuration in production mode.
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Outside production the same checks run and are LOGGED instead — all but
	// the secret checks, which Validate() has just refused on unless the
	// install opted in. Without this they execute only where APP_ENV says
	// production, so a development or test install never sees a configuration
	// regression and the first place it surfaces is a refused production boot.
	//
	// Warnings, not a refusal, because several of these are correct for a
	// development install, which may genuinely serve plain HTTP on a trusted
	// LAN. The point is that a regression is visible before production.
	if !cfg.IsProduction() {
		if problems := cfg.Problems(); len(problems) > 0 {
			logger.Warn("configuration would be REFUSED in production mode; running anyway because this is not a production environment",
				"environment", cfg.Server.Environment, "count", len(problems))
			for _, problem := range problems {
				logger.Warn("insecure configuration", "problem", problem)
			}
		}
	}

	// Advisories are couplings, not fail-closed conditions: they are logged in
	// EVERY environment and refuse nothing. Keep them out of Problems() — that
	// list is what Validate() (called above) turns into a fatal error in
	// production, and the backend runs under `restart: always`.
	for _, advisory := range cfg.Advisories() {
		logger.Warn("configuration advisory", "advisory", advisory)
	}

	logger.Info("configuration loaded successfully", "environment", cfg.Server.Environment)
	return cfg, nil
}

// setupLogger configures the application logger
func setupLogger() *slog.Logger {
	// Same inverted polarity as Config.IsProduction: unset, misspelled or an
	// unrecognized name all count as PRODUCTION. This used to be its own
	// `os.Getenv("APP_ENV") != "production"` compare — the pre-inversion rule —
	// so a typo gave Debug-level logging on an install every other guard treated
	// as production.
	isDev := !config.IsProductionEnvironment(os.Getenv("APP_ENV"))

	// LOG_LEVEL overrides the level without touching APP_ENV. Without it, the
	// only way to raise verbosity in production is to change APP_ENV, which
	// also drops fail-closed config validation and turns on full SQL logging
	// with interpolated parameter values — so in practice the level could not
	// be raised at all while something was wrong. The JSON-vs-text choice stays
	// keyed on the environment, which is the right coupling.
	logLevel := slog.LevelInfo
	if isDev {
		logLevel = slog.LevelDebug
	}
	if raw := strings.TrimSpace(os.Getenv("LOG_LEVEL")); raw != "" {
		var parsed slog.Level
		if err := parsed.UnmarshalText([]byte(raw)); err == nil {
			logLevel = parsed
		} else {
			defer func() {
				slog.Warn("ignoring unparseable LOG_LEVEL", "value", raw)
			}()
		}
	}

	// Create a handler with the appropriate level. ReplaceAttr is the
	// credential-header backstop: any attribute carrying a header map is
	// redacted before it is written, whichever call site logged it.
	opts := &slog.HandlerOptions{
		Level:       logLevel,
		ReplaceAttr: logging.RedactHeaderAttr,
	}

	// Use JSON handler in production, text handler in development
	var handler slog.Handler
	if isDev {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	// Wrapped so every context-aware log call (InfoContext / WarnContext /
	// ErrorContext / DebugContext) carries the request id and the account that
	// caused it, without a single call site changing. Without it `id` exists on
	// the access-log line and nowhere else, and no line anywhere names the
	// account — so a report from one account cannot be narrowed to that
	// account's requests at all.
	return slog.New(logging.NewContextHandler(handler))
}
