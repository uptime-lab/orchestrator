package config

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
)

const (
	// EnvProduction is the canonical name for the hardened mode. It is no
	// longer the trigger for fail-closed validation — that is now the DEFAULT.
	// See IsProduction.
	EnvProduction = "production"

	// insecureDefaultJWTSecret is the built-in JWT signing secret used only for
	// local development. Validate() rejects it in production.
	insecureDefaultJWTSecret = "default-secret-key-change-in-production"

	// InsecureDefaultSecretsKey is the built-in AES key used only for local
	// development when SECRETS_ENCRYPTION_KEY is unset. Validate() rejects an
	// empty key in production, so this is never reached there.
	InsecureDefaultSecretsKey = "orchestrator-insecure-dev-secret-encryption-key"

	// DefaultAuditEventRetentionDays is how long the audit trail is kept
	// when AUDIT_EVENT_RETENTION_DAYS is unset: a year of who did what.
	DefaultAuditEventRetentionDays = 365

	// decompressionRatioLimit is how many times larger than the upload cap a
	// decompressed image may be before the job is failed as a decompression
	// bomb. Deliberately generous: real OS images hit double-digit ratios
	// because their free space is zeroed, while a bomb reaches thousands.
	decompressionRatioLimit = 20
)

// Config holds all application configuration
type Config struct {
	Server       ServerConfig       `yaml:"server"`
	Database     DatabaseConfig     `yaml:"database"`
	JWT          JWTConfig          `yaml:"jwt"`
	OIDC         OIDCConfig         `yaml:"oidc"`
	GRPC         GRPCConfig         `yaml:"grpc"`
	Storage      StorageConfig      `yaml:"storage"`
	Provisioning ProvisioningConfig `yaml:"provisioning"`
	Secrets      SecretsConfig      `yaml:"secrets"`
	EEPROMSigner EEPROMSignerConfig `yaml:"eeprom_signer"`
	Terminal     TerminalConfig     `yaml:"terminal"`
	SSHAdopt     SSHAdoptConfig     `yaml:"ssh_adopt"`
	Workflow     WorkflowConfig     `yaml:"workflow"`
	Retention    RetentionConfig    `yaml:"retention"`
	Image        ImageConfig        `yaml:"image"`
	NodeFile     NodeFileConfig     `yaml:"node_file"`
	Member       MemberConfig       `yaml:"member"`
	Email        EmailConfig        `yaml:"email"`
	Quota        QuotaConfig        `yaml:"quota"`
	Monitoring   MonitoringConfig   `yaml:"monitoring"`
	Entitlement  EntitlementConfig  `yaml:"entitlement"`
}

// EntitlementConfig is the device entitlement handed in at boot — the
// air-gap and bootstrap path. A file applied through the API lives in the
// database instead; the newest valid one of the two wins. The free cap, the
// grace and every other term are signed fields of the file, not settings:
// there is nothing here that raises the cap without a signed file, on any
// install, in any environment.
type EntitlementConfig struct {
	// Document is the paste form of a signed entitlement file. A value that
	// does not verify is logged at boot and ignored; the install starts on
	// the free cap rather than refusing to boot.
	// env: ENTITLEMENT (or ENTITLEMENT_FILE, a path to a file holding it)
	Document string `yaml:"document"`
	// SubscriptionURL is where a device-cap refusal sends the operator.
	// Empty uses the product's own page.
	// env: ENTITLEMENT_SUBSCRIPTION_URL
	SubscriptionURL string `yaml:"subscription_url"`
	// AccountDeviceLimit is how many ADMITTED devices one account may hold,
	// checked at every admission in addition to the instance-wide cap. 0 (the
	// default) is no per-account limit. An account's own override
	// (accounts.device_limit, optionally until accounts.device_limit_until)
	// replaces it for that account while in force. It only bounds new
	// admissions: an account above it keeps every device it already has. The
	// instance operator's account is never bound by it.
	// env: ACCOUNT_DEVICE_LIMIT
	AccountDeviceLimit int `yaml:"account_device_limit"`
	// AccountDeviceLimitContact is where a tenant at its limit is told to
	// ask for more devices (typically an e-mail address), shown on its
	// subscription page and in the refusal. One line, at most 254
	// characters; a value that is not is ignored (Advisories says so).
	// env: ACCOUNT_DEVICE_LIMIT_CONTACT
	AccountDeviceLimitContact string `yaml:"account_device_limit_contact"`
}

// EffectiveAccountDeviceLimit is the per-account default admission enforces:
// AccountDeviceLimit, or 0 (none) for a negative value, which Advisories
// reports.
func (e EntitlementConfig) EffectiveAccountDeviceLimit() int {
	if e.AccountDeviceLimit < 0 {
		return 0
	}
	return e.AccountDeviceLimit
}

// EffectiveAccountDeviceLimitContact is the contact as shown: the configured
// value when it is one valid line, otherwise empty (Advisories reports it).
func (e EntitlementConfig) EffectiveAccountDeviceLimitContact() string {
	v := strings.TrimSpace(e.AccountDeviceLimitContact)
	if !entitlement.ValidAccountLimitContact(v) {
		return ""
	}
	return v
}

// accountLimitAdvisories says what the per-account limit is missing. None of
// it refuses a boot: an operator's typo must not take the control plane
// down, and an unset or invalid limit is today's behaviour (no limit).
func (e EntitlementConfig) accountLimitAdvisories() []string {
	var out []string
	if e.AccountDeviceLimit < 0 {
		out = append(out, "ACCOUNT_DEVICE_LIMIT is not a non-negative integer: no per-account device limit applies. Set a number of devices, or 0 for none")
	}
	raw := strings.TrimSpace(e.AccountDeviceLimitContact)
	if raw != "" && !entitlement.ValidAccountLimitContact(raw) {
		out = append(out, fmt.Sprintf("ACCOUNT_DEVICE_LIMIT_CONTACT is ignored: it must be one line of at most %d characters", entitlement.MaxAccountLimitContactLength))
	}
	if e.EffectiveAccountDeviceLimit() > 0 && e.EffectiveAccountDeviceLimitContact() == "" {
		out = append(out, "ACCOUNT_DEVICE_LIMIT is set without ACCOUNT_DEVICE_LIMIT_CONTACT: an account at its limit is told to ask the operator of this service, with no address to write to")
	}
	return out
}

// MonitoringConfig says who READS what this backend records.
//
// The backend detects and records instance anomalies unconditionally, by design:
// alerting policy belongs to an operator console outside this tree, and the
// backend never reads that tooling's settings, so the dependency stays one-way
// and the tooling can run with its own, narrower access. But an install with
// NO console then upserts into `instance_anomalies` with nothing reading it,
// silently and forever.
//
// The design asked for a startup WARN so that "nobody will be alerted here" is a
// STATED condition rather than an assumption. This is the one thing the backend
// cannot derive: nothing in this process can see whether a reader exists.
// So it is declared, and the default is the SAFE direction — an install that
// says nothing is assumed to have no reader and gets the warning. An install
// that does have one sets the variable and the line disappears.
type MonitoringConfig struct {
	// AnomalyReader names what consumes instance_anomalies. "admin-console" on
	// an install running it; anything else, including empty, means nothing does
	// ("none" says so on purpose and silences the startup warning).
	AnomalyReader string `yaml:"anomaly_reader"`
}

// AnomalyReaderAdminConsole is the one value that means the rows are read.
const AnomalyReaderAdminConsole = "admin-console"

// AnomalyReaderNone is an install's statement that nothing reads the rows and
// that this is known: an install that ships no admin console (the community
// compose) declares it, so its operator is not warned about a component the
// install does not have.
const AnomalyReaderNone = "none"

// AnomaliesAreRead reports whether anything consumes what the detectors write.
func (c *Config) AnomaliesAreRead() bool {
	return strings.EqualFold(strings.TrimSpace(c.Monitoring.AnomalyReader), AnomalyReaderAdminConsole)
}

// AnomalyReaderDeclaredAbsent reports whether the install stated that nothing
// reads the rows. Silence is not such a statement.
func (c *Config) AnomalyReaderDeclaredAbsent() bool {
	return strings.EqualFold(strings.TrimSpace(c.Monitoring.AnomalyReader), AnomalyReaderNone)
}

// QuotaConfig bounds what ONE tenant may take from a shared instance. It is the
// counterpart to MemberConfig: that one protects people outside the platform
// from a tenant, this one protects the other tenants.
//
// These DEFAULT ARMED, unlike the storage quotas, and the split is the same one
// the workflow guards already draw. A storage ceiling bounds what a tenant costs
// its own operator, so leaving it off on-prem is a defensible default. A request
// rate and a resource count decide whether one account can degrade everybody
// else's service, and "unlimited until an operator notices" is not a defensible
// default for that — the first time it matters is the time it is already
// happening. Every value is still 0 = unlimited for an operator who wants that.
type QuotaConfig struct {
	// APIRequestsPerSecond and APIBurst bound the AUTHENTICATED API per ACCOUNT
	// (not per user, and not per IP): the account is the tenancy unit, so one
	// tenant running a script from ten machines is still one tenant.
	//
	// The numbers are deliberately generous — a page load in this UI is tens of
	// requests, and a limiter that a real operator can hit is a limiter that gets
	// switched off. This bounds a runaway loop, not a busy human.
	// env: API_RATE_LIMIT_PER_ACCOUNT_RPS
	APIRequestsPerSecond float64 `yaml:"api_requests_per_second"`
	// env: API_RATE_LIMIT_PER_ACCOUNT_BURST
	APIBurst int `yaml:"api_burst"`

	// MaxNodesPerAccount caps the fleet one account may hold. Counted at the
	// point a node ROW is created, which is the one thing every adoption path
	// shares.
	// env: NODE_MAX_PER_ACCOUNT
	MaxNodesPerAccount int `yaml:"max_nodes_per_account"`
	// SerialLeaseDays bounds how long a hardware serial may stay bound to a
	// node that has never been ADMITTED — a WebUSB/SSH placeholder awaiting its
	// first connect, or a master-token node still pending approval — while
	// nothing has been seen from the device. Past it the hourly sweep releases
	// the FINGERPRINT (never the row), so the serial is free for the account
	// that actually holds the hardware. Binding a serial one does not own
	// therefore costs continuous, attributable heartbeats instead of one POST.
	// An admitted node is never touched. 0 disables. env: NODE_SERIAL_LEASE_DAYS
	SerialLeaseDays int `yaml:"serial_lease_days"`
	// MaxMembersPerAccount caps seats. Checked when an invite is SENT and again
	// when it is ACCEPTED, because an invite issued under the cap can be
	// accepted after it is reached — checking only the send would let an account
	// exceed the cap by pre-issuing invitations.
	// env: MEMBER_MAX_PER_ACCOUNT
	MaxMembersPerAccount int `yaml:"max_members_per_account"`
	// MaxSecretsPerAccount caps secrets. Node ATTRIBUTES are secrets too
	// (attr:<nodeID>:<key>), so an account with many nodes spends this budget
	// without ever visiting the Secrets page — which is why the default is sized
	// for many attributes per node rather than for hand-created secrets alone.
	// env: SECRET_MAX_PER_ACCOUNT
	MaxSecretsPerAccount int `yaml:"max_secrets_per_account"`

	// MaxAgentStreamsPerNode caps concurrent AUTHENTICATED agent streams for one
	// node. Per node and not per account: a fleet-wide bound is the one shape
	// that can take a whole account offline at once, and what this bounds is a
	// single compromised device.
	// env: AGENT_MAX_STREAMS_PER_NODE
	MaxAgentStreamsPerNode int `yaml:"max_agent_streams_per_node"`
	// AgentStreamMessagesPerSecond and AgentStreamBurst pace ONE agent stream's
	// inbound frames. Frames are PACED, never dropped — a dropped command result
	// leaves a node_command in_progress until the abandonment sweep, so slowing
	// a flooder is the only bound that cannot lose information.
	// env: AGENT_STREAM_RATE_LIMIT_RPS
	AgentStreamMessagesPerSecond float64 `yaml:"agent_stream_messages_per_second"`
	// env: AGENT_STREAM_RATE_LIMIT_BURST
	AgentStreamBurst int `yaml:"agent_stream_burst"`
	// MaxPreAuthStreams caps agent streams that have NOT yet authenticated,
	// process-wide (grpc/preauth_limits.go carries the arithmetic). It does
	// not depend on the edge's per-address connection bound, which is sized to
	// a site's whole fleet and admits far more than this gate holds.
	// Without PROXY protocol it is the ONLY backend bound, because the backend
	// never sees a client address; with AGENT_GRPC_PROXY_PROTOCOL=require the
	// per-source gate below also applies. Unlike the other quotas 0 is NOT
	// unlimited: it selects the code default, because an unbounded pre-auth
	// gate is the hole itself.
	// env: AGENT_MAX_PREAUTH_STREAMS
	MaxPreAuthStreams int `yaml:"max_preauth_streams"`
	// MaxPreAuthStreamsPerSource caps unauthenticated agent streams from ONE
	// client address. Armed ONLY when AGENT_GRPC_PROXY_PROTOCOL=require:
	// without PROXY protocol every peer is the proxy, and a per-source key
	// would BE the fleet-wide DoS it exists to prevent. The default (256) is
	// the backend's own number, not a copy of the edge's per-address
	// connection bound: one address holds at most a quarter of the 1024
	// global gate, so filling the gate costs FOUR addresses.
	// 0 selects the code default, as for MaxPreAuthStreams.
	// env: AGENT_MAX_PREAUTH_STREAMS_PER_SOURCE
	MaxPreAuthStreamsPerSource int `yaml:"max_preauth_streams_per_source"`

	// MaxUtilityAssignmentsPerAccount caps utility_assignments rows (the caps
	// doctrine: 409 on refusal, count fails open, 0 = unlimited).
	// env: UTILITY_MAX_ASSIGNMENTS_PER_ACCOUNT
	MaxUtilityAssignmentsPerAccount int `yaml:"max_utility_assignments_per_account"`
	// MaxConcurrentUtilityApplies bounds one account's live
	// apply_utility_config commands (docs/UTILITIES-PLAN.md §3.7). An apply
	// pins its node to maintenance for up to 30 minutes on the agent's single
	// command goroutine, so an unbounded OU-wide fan-out is a fleet-wide
	// outage from one click. Nodes past the bound are reported deferred, never
	// silently dropped. 0 = unlimited.
	// env: UTILITY_MAX_CONCURRENT_APPLIES
	MaxConcurrentUtilityApplies int `yaml:"max_concurrent_utility_applies"`
}

// NodeFileConfig bounds tracked-file storage. These are quotas, not safety
// bounds — the per-version size cap guards the gRPC frame and stays a constant
// in the domain — so they follow the repo's quota convention: 0 = unlimited.
// Unlike the other quota blocks they DEFAULT to the values the feature shipped
// with rather than to unlimited, because unbounded file history was never a
// deliberate on-prem behaviour, only an unconfigurable one.
type NodeFileConfig struct {
	// MaxVersionsPerPath is how much history one tracked file keeps.
	// env: NODE_FILE_MAX_VERSIONS_PER_PATH
	MaxVersionsPerPath int `yaml:"max_versions_per_path"`
	// MaxFilesPerNode is how many files one node may track.
	// env: NODE_FILE_MAX_PER_NODE
	MaxFilesPerNode int `yaml:"max_files_per_node"`
	// MaxBytesPerNode is one node's stored-content budget in bytes.
	// env: NODE_FILE_MAX_BYTES_PER_NODE
	MaxBytesPerNode int64 `yaml:"max_bytes_per_node"`
	// MaxBytesPerAccount is the same budget across every node of an account
	// (0 = unlimited, the default).
	// env: NODE_FILE_MAX_BYTES_PER_ACCOUNT
	MaxBytesPerAccount int64 `yaml:"max_bytes_per_account"`
}

// EmailConfig configures outbound mail (currently only verification links).
// When Enabled is false nothing is sent and email verification is inert: users
// stay unverified, which no flow depends on.
//
// A relay that authorizes by SENDER IP rather than credentials (for example
// the Google Workspace SMTP relay) needs no Username/Password; they exist for
// deployments using a plain authenticated submission host.
type EmailConfig struct {
	// Enabled turns outbound mail on. env: EMAIL_ENABLED
	Enabled bool `yaml:"enabled"`
	// Host/Port of the SMTP submission or relay host.
	// env: EMAIL_SMTP_HOST, EMAIL_SMTP_PORT
	Host string `yaml:"smtp_host"`
	Port int    `yaml:"smtp_port"`
	// Username/Password are optional: the Workspace relay authorizes by IP.
	// env: EMAIL_SMTP_USERNAME, EMAIL_SMTP_PASSWORD
	Username string `yaml:"smtp_username"`
	Password string `yaml:"smtp_password"`
	// FromAddress must be a real mailbox or alias in the sending domain, or
	// the relay rejects it. env: EMAIL_FROM_ADDRESS
	FromAddress string `yaml:"from_address"`
	// FromName is the display name shown in the recipient's client.
	// env: EMAIL_FROM_NAME
	FromName string `yaml:"from_name"`
	// StartTLS requires the connection to be upgraded before sending. Keep it
	// on: credentials and, more importantly, the verification link itself must
	// not cross the network in the clear. env: EMAIL_SMTP_STARTTLS
	StartTLS bool `yaml:"smtp_starttls"`
}

// Configured reports whether mail can actually be sent.
func (e EmailConfig) Configured() bool {
	return e.Enabled && strings.TrimSpace(e.Host) != "" && strings.TrimSpace(e.FromAddress) != ""
}

// Addr is the host:port to dial.
func (e EmailConfig) Addr() string {
	port := e.Port
	if port == 0 {
		port = 587
	}
	return fmt.Sprintf("%s:%d", e.Host, port)
}

// ImageConfig bounds per-tenant OS image usage. The storage ceilings default to
// unlimited; a shared install sets them.
type ImageConfig struct {
	// MaxUploadSize caps a single OS image upload in bytes, on top of the
	// global storage.max_file_size. 0 = no extra cap.
	// env: IMAGE_MAX_UPLOAD_SIZE
	MaxUploadSize int64 `yaml:"max_upload_size"`
	// MaxPerAccount caps how many images an account may have (0 = unlimited).
	// env: IMAGE_MAX_PER_ACCOUNT
	MaxPerAccount int `yaml:"max_per_account"`
	// MaxBytesPerAccount caps the TOTAL bytes an account's images occupy
	// (0 = unlimited). A count cap alone does not bound storage: three images
	// of 5 GiB each is 15 GiB, and the count is what an operator picks for
	// convenience while the bytes are what gets billed.
	//
	// It counts the stored file AND a retained pre-compression original, which
	// is the honest measure of what the account occupies — so an install that
	// keeps the original (IMAGE_COMPRESS_REPLACE_ORIGINAL off) is charged
	// roughly twice per image.
	// env: IMAGE_MAX_BYTES_PER_ACCOUNT
	MaxBytesPerAccount int64 `yaml:"max_bytes_per_account"`
	// MaxBytesInstance caps the total bytes of ALL accounts' images
	// (0 = unlimited). The per-account ceiling does not bound the instance:
	// self-service signup means N accounts is N times the per-account quota, on
	// an operator's bill. This is the backstop that does not depend on how many
	// accounts exist.
	// env: IMAGE_MAX_BYTES_INSTANCE
	MaxBytesInstance int64 `yaml:"max_bytes_instance"`
	// ReplaceOriginalOnCompress deletes the uploaded original after a
	// successful compression instead of retaining it as a safety copy
	// (halves per-image disk on space-constrained installs; the image can
	// no longer be re-compressed from the original).
	// env: IMAGE_COMPRESS_REPLACE_ORIGINAL
	ReplaceOriginalOnCompress bool `yaml:"replace_original_on_compress"`
	// AllowPlaintextURL permits dispatching install_os with an http:// image
	// URL while running in the hardened mode. An OS image is root-equivalent
	// on the target device and its only integrity check is a checksum that
	// travels in the same command, so fetching it in the clear lets anyone on
	// the path swap what the node installs. Off by default; on-prem installs
	// that genuinely serve images over plaintext on a trusted LAN set it
	// explicitly (or run with APP_ENV=development).
	// env: IMAGE_ALLOW_PLAINTEXT_URL
	AllowPlaintextURL bool `yaml:"allow_plaintext_url"`
	// MaxDecompressedSize caps how many bytes the compress pipeline will accept
	// out of a decompressor, in bytes. MaxUploadSize bounds the ARTIFACT, not
	// its expansion ratio, so without this a crafted .xz/.zst expands without
	// limit into storage. 0 derives the cap as
	// decompressionRatioLimit x MaxUploadSize, and with no upload cap either
	// it is DefaultImageMaxDecompressedSize — never unlimited. What a job may
	// STORE is bounded separately: its output counts against the byte quotas
	// like any other upload writer.
	// env: IMAGE_MAX_DECOMPRESSED_SIZE
	MaxDecompressedSize int64 `yaml:"max_decompressed_size"`
	// CompressionTimeoutMinutes bounds how long one compression or transcode
	// job may run once it holds a slot. Slots are shared by every account, so
	// a job with no deadline is a job that can hold the instance's compression
	// capacity for as long as its input lasts. A job past the deadline is
	// stopped, its partial output deleted and the image marked failed; the
	// uploaded file is untouched. 0 means the default, never unlimited.
	// env: IMAGE_COMPRESSION_TIMEOUT_MINUTES
	CompressionTimeoutMinutes int `yaml:"compression_timeout_minutes"`
	// CaptureInFlightWindowMinutes decides how long a started-but-not-yet-uploaded
	// capture occupies a slot in the per-account image COUNT (0 = never).
	//
	// The count query deliberately excludes transient capture records so an
	// abandoned one cannot permanently brick image creation — with a cap as low
	// as 1, a single stuck capture would. The cost of
	// that exclusion is that concurrent captures cannot see each other: a backup
	// playbook over N nodes has every record pass the same check at the same
	// moment, and the account ends with N images against a cap of 3.
	//
	// A window is what reconciles the two: a capture holds its slot only while it
	// is plausibly still streaming, and ages out of the count afterwards. Set it
	// longer than a legitimate whole-disk capture takes on the slowest link an
	// operator expects, and it costs nothing; set it shorter and concurrent
	// captures stop seeing each other again once they exceed it.
	// env: IMAGE_CAPTURE_INFLIGHT_WINDOW_MINUTES
	CaptureInFlightWindowMinutes int `yaml:"capture_inflight_window_minutes"`
	// MaxConcurrentUploads bounds how many multipart image uploads the server
	// stages at once, and MaxConcurrentUploadsPerAccount one account's share
	// of that. A multipart upload is parsed in full before the service sees
	// it — 32 MiB of it in memory, the rest spilled to TMPDIR, which sits on
	// the storage volume — and the count and byte quotas only see bytes that
	// have been WRITTEN, so without a bound one `image.manage` holder could
	// open N quota-sized uploads and hold N times the quota in TMPDIR while
	// every other tenant's uploads failed on a full disk.
	// This is a "does one tenant degrade everybody" guard, so it is armed by
	// default; 0 means the default, never unlimited. Refusal is a typed 429:
	// the caller's next step is to wait. env: IMAGE_MAX_CONCURRENT_UPLOADS,
	// IMAGE_MAX_CONCURRENT_UPLOADS_PER_ACCOUNT
	MaxConcurrentUploads           int `yaml:"max_concurrent_uploads"`
	MaxConcurrentUploadsPerAccount int `yaml:"max_concurrent_uploads_per_account"`
	// XZMemLimitMB bounds the memory the xz binary may use per invocation, in
	// MiB, for compression and for (multi-threaded) decompression. `xz -T0`
	// sizes its threads and dictionaries from PHYSICAL RAM, not from the
	// container's cgroup limit, so inside a memory-limited container an
	// unbounded run is an OOM kill of the whole backend rather than a slow job. xz
	// reduces threads, then the dictionary, to fit; an archive whose
	// dictionary alone exceeds the limit is refused with an xz error rather
	// than decoded. 0 means the default, never unlimited.
	// env: IMAGE_XZ_MEMLIMIT_MB
	//
	// CompressionMemoryBudgetMB bounds the SUM of those per-process limits
	// across every compression job running at once: a
	// transcode is a decoder AND an encoder, each bounded by XZMemLimitMB on
	// its own, and MaxConcurrentCompressions counts jobs, not processes — so
	// the container-wide ceiling was jobs × 2 × limit with nothing enforcing
	// it. A job takes its WHOLE weight (one or two process limits) before it
	// starts, never one process at a time, or two transcodes could each hold a
	// decoder and wait forever for an encoder. 0 = default: two process limits,
	// i.e. exactly one transcode. Never unlimited, and never below two process
	// limits (a transcode must be able to run at all).
	// env: IMAGE_COMPRESSION_MEMORY_BUDGET_MB
	//
	// The bound has a FLOOR (MinImageXZMemLimitMB): below it xz falls back to
	// its single-threaded encoder, which emits a SINGLE-BLOCK stream — the very
	// format the platform classifies as needing a repack (docs/OS-IMAGES.md).
	// On xz 5.8.3, 166 MiB keeps the multi-threaded encoder with one
	// thread, 165 MiB does not. A "safe-looking" 128 would have silently made
	// every platform compression single-block, so EffectiveXZMemLimitMB clamps
	// up and main.go logs it.
	XZMemLimitMB              int `yaml:"xz_memlimit_mb"`
	CompressionMemoryBudgetMB int `yaml:"compression_memory_budget_mb"`
	// MaxConcurrentCompressions bounds how many compression / transcode jobs
	// run at once. Each job is a two-process pipeline (a decoder feeding an
	// encoder through a pipe), and XZMemLimitMB bounds each PROCESS — so the
	// container's worst case is jobs x (encoder + decoder), not one limit.
	// With the 256 MiB default an encoder holds ~166 MiB and a `-9` vendor
	// decoder ~65 MiB: two jobs are ~462 MiB before the Go process is counted,
	// so a container that cannot hold that plus the Go process sets 1. 0 means
	// the default, never unlimited.
	// env: IMAGE_MAX_CONCURRENT_COMPRESSIONS
	MaxConcurrentCompressions int `yaml:"max_concurrent_compressions"`
	// MaxStagingBytes bounds the SUM of bytes admitted uploads may hold in
	// TMPDIR at once, server-wide. The slot counts bound how many, not how
	// much: N slots at a per-request cap of C permit N × C of spill, which can
	// exceed a small disk. Reservations are the declared Content-Length
	// (or the per-request cap for a chunked body), so the sum is what the
	// disk could be asked for. Armed by default (0 = default, never
	// unlimited); each install sets it from its own TMPDIR volume.
	// env: IMAGE_MAX_STAGING_BYTES
	MaxStagingBytes int64 `yaml:"max_staging_bytes"`
}

// Image upload admission and xz memory defaults. Exported so a Config built
// without getDefaultConfig (tests, a hand-written YAML) falls back to the SAME
// numbers rather than to "unlimited".
//
// 6 global / 2 per account: a multipart spill is bounded by the per-request
// cap (storage.max_file_size), so six in flight is the most staging space the
// server can be asked to hold at once, and two per account leaves a tenant a
// retry while its first upload streams without letting it take the pool. 256
// MiB for xz keeps one transcode (two process limits) plus the Go process and
// the multipart buffers inside a container well under 1 GiB; xz -6 single-threaded needs about 94 MiB, so
// the limit costs compression threads, not the ability to compress.
const (
	DefaultImageMaxConcurrentUploads           = 6
	DefaultImageMaxConcurrentUploadsPerAccount = 2
	DefaultImageXZMemLimitMB                   = 256
	// MinImageXZMemLimitMB is the multi-block floor; see ImageConfig.XZMemLimitMB.
	MinImageXZMemLimitMB                  = 192
	DefaultImageMaxConcurrentCompressions = 2
	DefaultImageMaxStagingBytes           = int64(32) << 30
	// DefaultImageMaxDecompressedSize is the expansion cap when neither
	// IMAGE_MAX_DECOMPRESSED_SIZE nor IMAGE_MAX_UPLOAD_SIZE says otherwise:
	// above any single-board OS image or the disks they ship for, far below
	// what a crafted archive can expand to.
	DefaultImageMaxDecompressedSize = int64(64) << 30
	// DefaultImageCompressionTimeoutMinutes is the per-job deadline: a
	// single-threaded xz -6 (what the memory floor leaves a small host) needs
	// well under three hours for a 64 GiB stream.
	DefaultImageCompressionTimeoutMinutes = 180
	// CompressionGoHeadroomMB is the room the Go process itself needs beside
	// the compression budget inside the backend's memory limit. main.go
	// checks budget + headroom against the cgroup limit at boot, and the
	// community compose's test checks its own defaults against the same sum.
	CompressionGoHeadroomMB = 128
)

// EffectiveMaxConcurrentUploads returns the global upload slot count, with 0
// and negatives meaning the default rather than unlimited.
func (i ImageConfig) EffectiveMaxConcurrentUploads() int {
	if i.MaxConcurrentUploads <= 0 {
		return DefaultImageMaxConcurrentUploads
	}
	return i.MaxConcurrentUploads
}

// EffectiveMaxConcurrentUploadsPerAccount returns one account's upload slot
// share, with 0 and negatives meaning the default rather than unlimited.
func (i ImageConfig) EffectiveMaxConcurrentUploadsPerAccount() int {
	if i.MaxConcurrentUploadsPerAccount <= 0 {
		return DefaultImageMaxConcurrentUploadsPerAccount
	}
	return i.MaxConcurrentUploadsPerAccount
}

// EffectiveCompressionMemoryBudgetMB returns the container-wide compression
// memory budget in MiB: the configured value, never below two process limits,
// and two process limits when unset.
func (i ImageConfig) EffectiveCompressionMemoryBudgetMB() int {
	floor := 2 * i.EffectiveXZMemLimitMB()
	if i.CompressionMemoryBudgetMB < floor {
		return floor
	}
	return i.CompressionMemoryBudgetMB
}

// CompressionMemoryBudgetClamped reports that a configured budget was raised
// to the two-process floor, so the boot log can say so.
func (i ImageConfig) CompressionMemoryBudgetClamped() bool {
	return i.CompressionMemoryBudgetMB > 0 && i.CompressionMemoryBudgetMB < 2*i.EffectiveXZMemLimitMB()
}

// EffectiveXZMemLimitMB returns the xz memory limit in MiB, with 0 and
// negatives meaning the default rather than unlimited.
func (i ImageConfig) EffectiveXZMemLimitMB() int {
	if i.XZMemLimitMB <= 0 {
		return DefaultImageXZMemLimitMB
	}
	if i.XZMemLimitMB < MinImageXZMemLimitMB {
		return MinImageXZMemLimitMB
	}
	return i.XZMemLimitMB
}

// XZMemLimitClamped reports whether the configured xz bound was raised to the
// multi-block floor, so main.go can say so once at boot.
func (i ImageConfig) XZMemLimitClamped() bool {
	return i.XZMemLimitMB > 0 && i.XZMemLimitMB < MinImageXZMemLimitMB
}

// EffectiveMaxConcurrentCompressions returns the compression job bound, with
// 0 and negatives meaning the default, never unlimited.
func (i ImageConfig) EffectiveMaxConcurrentCompressions() int {
	if i.MaxConcurrentCompressions <= 0 {
		return DefaultImageMaxConcurrentCompressions
	}
	return i.MaxConcurrentCompressions
}

// EffectiveMaxStagingBytes returns the server-wide staging byte budget, with 0
// and negatives meaning the default, never unlimited.
func (i ImageConfig) EffectiveMaxStagingBytes() int64 {
	if i.MaxStagingBytes <= 0 {
		return DefaultImageMaxStagingBytes
	}
	return i.MaxStagingBytes
}

// CaptureInFlightWindow returns how long an in-flight capture counts toward the
// per-account image cap, or 0 when in-flight captures are not counted at all.
func (i ImageConfig) CaptureInFlightWindow() time.Duration {
	if i.CaptureInFlightWindowMinutes <= 0 {
		return 0
	}
	return time.Duration(i.CaptureInFlightWindowMinutes) * time.Minute
}

// DecompressionLimit returns the byte cap for decompressed image data; it is
// never 0. An explicit MaxDecompressedSize always wins; otherwise it is
// derived from the upload cap with generous headroom, because OS images
// legitimately compress well (a mostly-zeroed filesystem reaches ~15:1) and a
// false positive here fails an operator's real image. With no upload cap
// either, it is DefaultImageMaxDecompressedSize.
func (i ImageConfig) DecompressionLimit() int64 {
	if i.MaxDecompressedSize > 0 {
		return i.MaxDecompressedSize
	}
	if i.MaxUploadSize > 0 {
		return i.MaxUploadSize * decompressionRatioLimit
	}
	return DefaultImageMaxDecompressedSize
}

// CompressionTimeout returns the per-job deadline, with 0 and negatives
// meaning the default, never unlimited.
func (i ImageConfig) CompressionTimeout() time.Duration {
	if i.CompressionTimeoutMinutes <= 0 {
		return time.Duration(DefaultImageCompressionTimeoutMinutes) * time.Minute
	}
	return time.Duration(i.CompressionTimeoutMinutes) * time.Minute
}

// MemberConfig bounds what one tenant can make the platform do on its behalf to
// people outside it.
//
// Inviting a colleague sends mail through the operator's own DKIM-aligned relay
// to an address the tenant chooses, so an unbounded invite endpoint hands every
// tenant a share of the operator's domain reputation. Nothing here protects
// the account; it protects everyone else's mail from it.
type MemberConfig struct {
	// MaxInvitesPerDay caps invite sends per account per rolling 24 hours,
	// counting both a first send and a resend (0 = unlimited).
	// env: MEMBER_MAX_INVITES_PER_DAY
	MaxInvitesPerDay int `yaml:"max_invites_per_day"`
}

// RetentionConfig bounds the two tables nothing else deletes from: device
// command history and the audit trail. Command history defaults to 0 =
// unlimited — a command sweep that ran unasked would be a surprise on the
// filesystem that breaks first. The audit trail defaults to a year
// (DefaultAuditEventRetentionDays); 0 keeps it forever.
type RetentionConfig struct {
	// NodeCommandDays is how long TERMINAL device commands are kept. Commands
	// still awaiting delivery or a settle are never swept regardless of age.
	// env: NODE_COMMAND_RETENTION_DAYS
	NodeCommandDays int `yaml:"node_command_days"`
	// AuditEventDays is how long audit events are kept, the install default
	// an account's own value (accounts.audit_retention_days) overrides. 0
	// keeps them forever; a positive value is held between
	// audit_event.MinRetentionDays and MaxRetentionDays.
	// env: AUDIT_EVENT_RETENTION_DAYS
	AuditEventDays int `yaml:"audit_event_days"`
}

// WorkflowConfig bounds workflow-execution growth per tenant. A per-minute cron
// playbook creates executions forever, so both the in-flight set and the stored
// history need caps.
type WorkflowConfig struct {
	// ExecutionRetentionDays is how long finished (terminal-status) executions
	// are kept before the retention job deletes them together with their
	// per-node state and workflow-issued command history. 0 disables retention.
	// env: WORKFLOW_EXECUTION_RETENTION_DAYS
	ExecutionRetentionDays int `yaml:"execution_retention_days"`
	// MaxActiveExecutionsPerAccount caps an account's pending+running
	// executions; StartExecution (manual and scheduled alike) is rejected at
	// the cap. 0 disables the cap.
	// env: WORKFLOW_MAX_ACTIVE_EXECUTIONS_PER_ACCOUNT
	MaxActiveExecutionsPerAccount int `yaml:"max_active_executions_per_account"`
	// MaxCronPlaybooksPerAccount caps how many schedule-triggered playbooks the
	// scheduler honors per account per tick (oldest first, skips logged); the
	// rest never fire. 0 disables the cap.
	// env: WORKFLOW_MAX_CRON_PLAYBOOKS_PER_ACCOUNT
	MaxCronPlaybooksPerAccount int `yaml:"max_cron_playbooks_per_account"`
	// MaxRunningNodesPerAccount caps an account's pending+running EXECUTION
	// NODES, which is the thing that actually consumes the 500 process-global
	// workflow workers. 0 disables the cap.
	//
	// The execution cap above does not bound this, and that is the whole point
	// of having both: 20 executions × an unbounded node count is unbounded, and
	// node rows are free. One tenant could park every worker indefinitely.
	// env: WORKFLOW_MAX_RUNNING_NODES_PER_ACCOUNT
	MaxRunningNodesPerAccount int `yaml:"max_running_nodes_per_account"`
}

// SSHAdoptConfig gates the "adopt a device over SSH" flow: the platform connects
// outbound to an already-running device, installs the agent and registers the
// node. It is a powerful outbound capability, so it is OFF unless explicitly
// enabled (ssh_adopt.enabled / SSH_ADOPT_ENABLED).
type SSHAdoptConfig struct {
	Enabled bool `yaml:"enabled"`
	// AllowedCDNBaseURL is the ONLY base URL the device is told to fetch the agent
	// package from (SSRF allowlist). Empty disables agent download over SSH.
	AllowedCDNBaseURL string `yaml:"allowed_cdn_base_url"`
	// AdoptionFilePath is where the adoption blob is written on the target device
	// (the installed agent's systemd unit points --adoption-file here).
	AdoptionFilePath string `yaml:"adoption_file_path"`
	// HostKeyPolicy is "tofu" or "strict" and governs the PREFLIGHT's first
	// contact only. Neither is a silent ignore, and the server never stores a
	// fingerprint of its own (a reimaged device changes its key; a stored pin
	// would refuse the legitimate reinstall). Trust travels with
	// the REQUESTS: a reachable preflight answers with a short-lived
	// host_key_ticket (HMAC over account, user, host, port, vetted address
	// and the key it saw), and the adoption must present that ticket or a
	// host_key_fingerprint known out of band. The adoption NEVER takes a key
	// on first use, whatever this setting says; it is the call that offers
	// credentials, so a request without a binding is a typed 400. "tofu"
	// (the default) lets a preflight WITHOUT an expectation connect on first
	// use and marks its result first_contact; "strict" refuses such a
	// preflight outright, so every one must carry
	// expected_host_key_fingerprint obtained out of band. The UI never sends
	// a fingerprint it did not get from an earlier test, so "strict" is an
	// API-only mode. A preflight that carries an expectation always connects
	// strictly against it; only accept_new_host_key lets it take a different
	// key, and that reset is audited with both fingerprints.
	HostKeyPolicy string `yaml:"host_key_policy"`
	// Timeouts in seconds; zero falls back to built-in defaults.
	// CommandTimeoutSeconds bounds EACH remote command of a preflight or an
	// adoption run; the install step (package download, dpkg/apt) carries its
	// own longer bound in the service, because a 60 s blanket fails a real
	// `apt-get -f` on a slow Pi.
	ConnectTimeoutSeconds   int `yaml:"connect_timeout_seconds"`
	CommandTimeoutSeconds   int `yaml:"command_timeout_seconds"`
	AgentWaitTimeoutSeconds int `yaml:"agent_wait_timeout_seconds"`
	// AllowAgentAuth permits authenticating with a forwarded ssh-agent (e.g. a
	// YubiKey forwarded over `ssh -R`). The agent socket lives on the BACKEND
	// host, so under multi-user RBAC this lends the forwarding operator's keys
	// to every user allowed to SSH-adopt — an on-prem/single-team affordance.
	// OFF by default; SaaS-style multi-tenant installs must keep it off.
	AllowAgentAuth bool `yaml:"allow_agent_auth"`
	// AgentSockDir, when set, switches agent auth to PER-USER sockets: the
	// request's authenticated user maps to <dir>/<user-id>.sock, so each
	// operator can only use an agent they forwarded themselves. Empty keeps the
	// legacy single shared socket from SSH_AUTH_SOCK (single-operator mode).
	AgentSockDir string `yaml:"agent_sock_dir"`
	// AllowedHostCIDRs restricts which hosts can be preflighted/adopted (the
	// device subnets). Empty = no restriction. Multi-tenant installs should set
	// this — Preflight is otherwise an authenticated internal-network scanner.
	AllowedHostCIDRs []string `yaml:"allowed_host_cidrs"`
}

// TerminalConfig gates the browser web-SSH terminal. It is a remote-shell
// capability, so it is OFF unless explicitly enabled (terminal.enabled /
// TERMINAL_ENABLED).
type TerminalConfig struct {
	Enabled bool `yaml:"enabled"`
	// MaxSessionsPerUser caps concurrent web-terminal sessions one user may
	// hold across the instance. Login sessions have had this cap for a while
	// (JWT.MaxSessionsPerUser); the terminal had none, and each open session
	// pins a goroutine pair, a PTY on the device and a slot in the node's
	// frame buffer. Refusing the (N+1)th, rather than LRU-evicting like
	// logins, is deliberate: silently killing an operator's live root shell
	// mid-command is worse than telling them to close one.
	// 0 = unlimited. env: TERMINAL_MAX_SESSIONS_PER_USER
	MaxSessionsPerUser int `yaml:"max_sessions_per_user"`
	// MaxPendingHandshakes bounds sockets that have been upgraded but have not
	// yet presented a token, across the whole instance — the number a stranger
	// controls, since the per-user cap above is keyed on a user id the caller
	// has not shown yet. MaxPendingHandshakesPerIP is the same bound per
	// trusted client address (Echo's IPExtractor trusts only the proxy), so
	// one source can saturate only its own share of the global pool. Neither
	// has an "unlimited": 0 or a
	// negative value means the code default, because the absence of this
	// bound is the vulnerability. env: TERMINAL_MAX_PENDING_HANDSHAKES,
	// TERMINAL_MAX_PENDING_HANDSHAKES_PER_IP
	MaxPendingHandshakes      int `yaml:"max_pending_handshakes"`
	MaxPendingHandshakesPerIP int `yaml:"max_pending_handshakes_per_ip"`
}

// Terminal pre-auth admission defaults. Exported so the handler that
// enforces them falls back to the SAME numbers when handed a Config built
// without getDefaultConfig (tests), rather than keeping a second copy.
//
// 64 mirrors the account socket's unauthorised bound. 8 per address is well
// above what one operator produces — a handshake authenticates within
// milliseconds, so even a browser reopening several tabs at once holds one or
// two — and low enough that a flooding source reaches its own ceiling at an
// eighth of the global pool.
const (
	DefaultTerminalMaxPendingHandshakes      = 64
	DefaultTerminalMaxPendingHandshakesPerIP = 8
)

// EEPROMSignerConfig points at the isolated eeprom-signer service that produces
// per-device signed EEPROM images (network-first Variant B). Empty URL disables
// on-platform EEPROM provisioning.
type EEPROMSignerConfig struct {
	URL   string `yaml:"url"`   // e.g. http://eeprom-signer:9099
	Token string `yaml:"token"` // optional bearer token (matches signer SIGNER_TOKEN)
}

// insecureEnvironments are the environment names that OPT OUT of fail-closed
// validation. Anything else — including unset, a typo, or a new name someone
// invents — is treated as production.
var insecureEnvironments = map[string]bool{
	"development": true,
	"dev":         true,
	"local":       true,
	"test":        true,
}

// IsProduction reports whether insecure defaults are rejected.
//
// The polarity here is deliberately inverted from the obvious one: an
// UNRECOGNIZED or UNSET environment means production. Previously this was a
// string compare against "production", so an unset or misspelled APP_ENV
// silently disabled every guard below — the server booted looking healthy while
// signing tokens with the publicly known built-in JWT secret, which is a full
// auth bypass by token forgery (and the same key derives the CDN and EEPROM
// capability HMACs, so the whole trust hierarchy went with it).
//
// Opting into insecure defaults must therefore be an explicit, correctly
// spelled act. A deployment that means to run loose says so; one that says
// nothing gets the safe answer.
func (c *Config) IsProduction() bool {
	return IsProductionEnvironment(c.Server.Environment)
}

// IsProductionEnvironment applies the same inverted polarity to a bare
// environment name, for callers that run BEFORE the config is loaded — chiefly
// setupLogger, which needs a level before there is a Config to ask. It exists so
// the rule has exactly one definition: setupLogger carried its own
// `os.Getenv("APP_ENV") != "production"` compare, i.e. the pre-inversion
// polarity this function was written to replace, which meant a misspelled
// APP_ENV produced Debug-level logging on an install the rest of the config
// treated as production.
func IsProductionEnvironment(name string) bool {
	return !insecureEnvironments[strings.ToLower(strings.TrimSpace(name))]
}

// minProductionSecretLength is the floor for a secret this platform signs or
// encrypts with. Not a cryptographic threshold — HMAC-SHA256 accepts any length
// — but a usability one: a generated value (`openssl rand`) is 32+ characters,
// while a human-typed phrase usually is not. 32 separates the two without
// rejecting anything an operator would deliberately choose.
const minProductionSecretLength = 32

// weakSecretMarkers are substrings that appear in the placeholder secrets this
// repository, its example config and its documentation have shipped.
//
// The check they replace compared against ONE literal
// (insecureDefaultJWTSecret), while `config/config.yaml.example` ships
// "your-secret-key-change-in-production" and a local development config
// typically carries "local-dev-secret" — both of which passed. A deployment
// that mounts its config directory and points CONFIG_PATH at it makes dropping
// the example into place the natural operator move, and that produced a
// publicly known signing key with no error anywhere.
//
// Deliberately not a general entropy test: substring matching cannot be argued
// with, and a false positive is a refused boot with a message that names the
// exact reason.
var weakSecretMarkers = []string{
	"change-in-production",
	"change_in_production",
	"changeme",
	"change-me",
	"your-secret",
	"dev-secret",
	"local-dev",
	"insecure",
	"placeholder",
}

// CDNSigningSecret is the base secret every cdnsign.DeriveKey call site must
// use. It exists so the fallback lives in ONE place: a dedicated
// CDN_SIGNING_KEY when configured, the JWT secret otherwise (which is what
// every install ran before the split). Callers still derive with domain
// separation, so the configured value is never itself the HMAC key.
func (c *Config) CDNSigningSecret() string {
	if key := strings.TrimSpace(c.Secrets.CDNSigningKey); key != "" {
		return key
	}
	return c.JWT.SecretKey
}

// weakSecretProblem returns an operator-facing reason the value must not be used
// in production, or "" if it is acceptable.
func weakSecretProblem(name, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return name + " is unset; set a strong unique secret (e.g. `openssl rand -hex 32`)"
	}
	lowered := strings.ToLower(trimmed)
	for _, marker := range weakSecretMarkers {
		if strings.Contains(lowered, marker) {
			return fmt.Sprintf("%s contains %q, so it is one of the placeholder values shipped in this repository and is publicly known; set a strong unique secret (e.g. `openssl rand -hex 32`)", name, marker)
		}
	}
	if len(trimmed) < minProductionSecretLength {
		return fmt.Sprintf("%s is %d characters; production requires at least %d (e.g. `openssl rand -hex 32`)",
			name, len(trimmed), minProductionSecretLength)
	}
	return ""
}

// Validate fails closed on an insecure configuration.
//
// In production every entry of Problems() refuses the boot, so an operator
// cannot ship a publicly forgeable JWT secret, unencrypted secrets or a
// plaintext agent endpoint by accident. Outside production only the SECRET
// checks refuse, and only until the install states ALLOW_INSECURE_DEV_SECRETS
// (see ServerConfig.AllowInsecureDevSecrets for why that is not production's
// business); the rest of the list is the caller's to log, because plaintext
// HTTP on a trusted LAN is what a development install legitimately is.
func (c *Config) Validate() error {
	if c.IsProduction() {
		if problems := c.Problems(); len(problems) > 0 {
			return fmt.Errorf("insecure configuration for production mode:\n  - %s", strings.Join(problems, "\n  - "))
		}
		return nil
	}
	if c.Server.AllowInsecureDevSecrets {
		return nil
	}
	if problems := c.secretProblems(); len(problems) > 0 {
		return fmt.Errorf("refusing to boot on a weak or placeholder secret (APP_ENV=%q is not production, but a published key is still a published key):\n  - %s\nGenerate real values, or set ALLOW_INSECURE_DEV_SECRETS=true to run a development install on these deliberately — never on a reachable server",
			c.Server.Environment, strings.Join(problems, "\n  - "))
	}
	return nil
}

// secretProblems is the subset of Problems() that no environment may run on
// unknowingly: the three values this process signs, encrypts and mints
// capabilities with. ONE definition, consumed by Problems() (the whole list)
// and by Validate() outside production (this subset alone), so the two can
// never disagree about which secrets count.
func (c *Config) secretProblems() []string {
	var problems []string
	if problem := weakSecretProblem("JWT_SECRET_KEY", c.JWT.SecretKey); problem != "" {
		// A forgeable JWT secret is a full authentication bypass — and it
		// takes the CDN and EEPROM capability HMACs with it whenever
		// CDN_SIGNING_KEY is unset, since they then derive from this value.
		problems = append(problems, problem)
	}
	// MANDATORY: unset or blank is a problem, never a silent JWT-derived
	// fallback. The fallback still exists in CDNSigningSecret for a
	// development install that opted in, but nothing else may run on it: the
	// derivation (cdnsign.DeriveKey over the JWT secret) is in the source, so
	// any JWT-secret disclosure also yields the capability that fetches a
	// node's adoption blob. The value has to agree with nothing outside this
	// process, so a fresh install can generate one on the spot
	// (`server --init-secrets`).
	if strings.TrimSpace(c.Secrets.CDNSigningKey) == "" {
		problems = append(problems,
			"CDN_SIGNING_KEY is unset; the CDN, image-download and per-serial EEPROM capabilities would derive from the JWT secret. Set CDN_SIGNING_KEY to a dedicated random value (`openssl rand -hex 32`), or point CDN_SIGNING_KEY_FILE at a file holding one")
	} else if problem := weakSecretProblem("CDN_SIGNING_KEY", c.Secrets.CDNSigningKey); problem != "" {
		problems = append(problems, problem)
	}
	if problem := weakSecretProblem("SECRETS_ENCRYPTION_KEY", c.Secrets.EncryptionKey); problem != "" {
		problems = append(problems, problem+" (secret values, provisioning passwords and tracked file contents are encrypted with it)")
	}
	return problems
}

// Problems lists every fail-closed condition, REGARDLESS of environment.
//
// Split out from Validate because a check that runs only in production is a
// check nobody tests against: a development install sets `APP_ENV=development`,
// and every guard here sat behind an early return on that, so the sole place
// this code ever executed was a production container booting — meaning a config
// regression was discovered as a failed production deploy.
//
// Flipping a development install to `production` is NOT the fix and must not be
// attempted: one that serves plain HTTP on a trusted LAN with no upstream TLS
// terminator would be refused by production validation, legitimately. Computing
// the list always and letting the caller decide how loud to be is what gives
// such an install the regression signal without breaking it. The secret
// checks are the one part that refuses everywhere (secretProblems).
func (c *Config) Problems() []string {
	problems := c.secretProblems()

	// The agent channel must be DECLARED, not inferred. Three shapes are
	// supported and an undeclared one is refused naming all three, because
	// a first install with no reverse proxy used to read this refusal as a
	// demand for a certificate it did not have; the declaration for a
	// trusted LAN is what lets it boot on purpose (and under an advisory,
	// see Advisories).
	if !c.GRPC.TLSDirect() && !c.GRPC.TLSTerminatedExternally && !c.GRPC.PlaintextOK {
		problems = append(problems, "the agent command channel would run in plaintext with nothing declared about it (device credentials and commands cross it). Declare one of the supported shapes: GRPC_TLS_TERMINATED_EXTERNALLY=true when a reverse proxy terminates TLS on the agent port (and sends PROXY protocol v2 with AGENT_GRPC_PROXY_PROTOCOL=require, so the per-source bound sees real addresses); AGENT_GRPC_PLAINTEXT_OK=true for a trusted LAN where devices reach this host without TLS; or GRPC_TLS_CERT_FILE and GRPC_TLS_KEY_FILE for TLS at the server")
	}
	// An enum this loader does not recognise is refused, never defaulted: a
	// typo in AGENT_GRPC_PROXY_PROTOCOL would otherwise leave the per-source
	// pre-auth gate silently inert on an install that believes it is bounded.
	// The gRPC listener asks the same method at Start, so outside production
	// (where this is only logged) the boot still fails closed on it.
	if problem := c.GRPC.ProxyProtocolProblem(); problem != "" {
		problems = append(problems, problem)
	}
	// An empty SERVER_INVITE_CODE is deliberately NOT a problem any more: from
	// the first account on it means self-registration is CLOSED (the auth
	// service's admitSignup), and the empty install admits its first owner
	// without a code. A code is what an operator sets to let further
	// organisations register, so its absence is the safe default, not an
	// open door. SERVER_OPEN_REGISTRATION is the explicit way to open it, and
	// its couplings are advisories (see Advisories), never refusals.
	// Mail without STARTTLS would put the verification link — a bearer
	// credential for the account — on the wire in the clear. An enabled mailer
	// with no way to build an absolute link would send a broken one.
	if c.Email.Enabled {
		if !c.Email.StartTLS {
			problems = append(problems, "EMAIL_SMTP_STARTTLS is off; verification links would cross the network unencrypted")
		}
		if strings.TrimSpace(c.Server.AppBaseURL) == "" {
			problems = append(problems, "APP_BASE_URL is unset while email is enabled; verification links would have no origin to point at")
		}
	}
	// A wildcard CORS origin combined with credentials lets any site drive the
	// authenticated API from a victim's browser.
	//
	// Validate the EFFECTIVE policy, not the configured one: with CORS.Enabled
	// false the server does not disable CORS, it installs AllowOrigins ["*"]
	// (see http_server.NewServer), so checking only the configured block missed
	// exactly the case where the wildcard actually ships.
	if c.Server.CORS.Enabled {
		if c.Server.CORS.AllowCredentials {
			for _, o := range c.Server.CORS.AllowedOrigins {
				if strings.TrimSpace(o) == "*" {
					problems = append(problems, "CORS allows credentials with a wildcard origin; pin CORS_ALLOWED_ORIGINS to the app origin(s) in production")
					break
				}
			}
		}
		if len(c.Server.CORS.AllowedOrigins) == 0 {
			problems = append(problems, "CORS is enabled with no allowed origins; set CORS_ALLOWED_ORIGINS to the app origin(s)")
		}
	} else {
		problems = append(problems, "CORS is disabled, which makes the server fall back to a wildcard origin; enable it and pin CORS_ALLOWED_ORIGINS to the app origin(s) in production")
	}

	return problems
}

// Advisories lists configuration couplings worth saying out loud that are NOT
// fail-closed conditions. It exists so the two lists cannot be confused.
//
// **Nothing here may ever move into Problems().** Problems() is what Validate()
// refuses to boot on in production, and the backend runs under `restart:
// always` — an advisory promoted to a problem would crash-loop a production
// install, and a development install could not have caught it, because it only
// LOGS what production refuses to boot on. That asymmetry is the whole reason
// this is a separate
// method rather than one more append in Problems(); `advisories_test.go`
// asserts the separation.
//
// The caller logs these at Warn regardless of environment.
func (c *Config) Advisories() []string {
	var advisories []string

	// The unauthenticated per-serial boot path is acceptable only ON THE BASIS
	// that the failsafe is off: with it armed, someone who knows a serial can
	// spend the counter on an OFFLINE node (the liveness guard in
	// RegisterNetbootWithhold, node/service.go, declines to count a withhold
	// for a node that heartbeated in the last five minutes) and divert its
	// next boot into recovery. The acceptance and the setting live in
	// different places with nothing joining them, so this advisory is the
	// join. main.go's loadConfig is the fatal call site for Validate(); this
	// warning deliberately is not one.
	if c.Provisioning.BootFailsafeThreshold > 0 {
		advisories = append(advisories, fmt.Sprintf(
			"PROVISIONING_BOOT_FAILSAFE_THRESHOLD=%d arms the boot failsafe; the unauthenticated per-serial boot path is safe only while it is OFF, because an attacker who knows a serial can divert an already-offline node's next boot into recovery. Set it to 0 unless that trade-off is acceptable for this install",
			c.Provisioning.BootFailsafeThreshold))
	}

	// A plaintext agent channel by declaration is ONE advisory, in every
	// mode: the declaration is what lets production boot, and the line is
	// what tells a log reader that device credentials and commands cross
	// that wire readable. Silent when the wire is TLS anyway (the declaration
	// is then inert) and absent when nothing was declared — that case is a
	// Problems() refusal and must not be said twice.
	if c.GRPC.PlaintextByDeclaration() {
		advisories = append(advisories,
			"the agent channel is served in PLAINTEXT by declaration (AGENT_GRPC_PLAINTEXT_OK=true): every device credential, command and terminal byte crosses the network readable. Acceptable only on a trusted LAN; put TLS in front of the agent port and declare GRPC_TLS_TERMINATED_EXTERNALLY=true, or give the server GRPC_TLS_CERT_FILE and GRPC_TLS_KEY_FILE, before any device reaches it over a network you do not own")
	}

	// The development opt-in is reported in both directions. In production it
	// did nothing, and an operator who set it to get past a refusal must hear
	// that here rather than keep looking for a typo; outside production it is
	// the one line in the log that says this process signs tokens with a key
	// anyone can read in this repository. Silent when it changes nothing, so
	// a development install on real secrets is not nagged.
	if c.Server.AllowInsecureDevSecrets {
		switch {
		case c.IsProduction():
			advisories = append(advisories,
				"ALLOW_INSECURE_DEV_SECRETS is set but has no effect in production; the secret checks refuse the boot regardless of it")
		case len(c.secretProblems()) > 0:
			advisories = append(advisories,
				"running on a weak or placeholder secret because ALLOW_INSECURE_DEV_SECRETS=true; every token this process signs and every value it encrypts is forgeable or readable by anyone who has the source. Acceptable only on a machine nothing else can reach")
		}
	}

	advisories = append(advisories, c.Server.registrationAdvisories()...)
	advisories = append(advisories, c.Entitlement.accountLimitAdvisories()...)
	// Open registration admits only accounts that confirm their address, so
	// without mail it admits nobody: sign-up is closed instead, and this
	// says why.
	if c.Server.OpenRegistration && !c.CanVerifyEmail() {
		advisories = append(advisories,
			"SERVER_OPEN_REGISTRATION=true but this install cannot send the confirmation mail an open sign-up must be verified with (EMAIL_ENABLED with EMAIL_SMTP_HOST and EMAIL_FROM_ADDRESS, and APP_BASE_URL): self-registration is CLOSED until mail works")
	}

	return advisories
}

// registrationAdvisories says out loud what open registration costs and
// what it overrides. Neither is a refusal: opening sign-up is the
// operator's call, like any per-account cost ceiling left at unlimited.
func (s ServerConfig) registrationAdvisories() []string {
	if !s.OpenRegistration {
		return nil
	}
	var advisories []string
	if s.MaxAccounts == 0 {
		advisories = append(advisories,
			"SERVER_OPEN_REGISTRATION=true with SERVER_MAX_ACCOUNTS=0: anyone who reaches the sign-up page can create accounts without limit, and every account carries the full per-account quotas on this server's storage and database. Set SERVER_MAX_ACCOUNTS to the number of organisations this install is meant to host")
	}
	if s.InviteCode != "" {
		advisories = append(advisories,
			"SERVER_INVITE_CODE is set but unused: SERVER_OPEN_REGISTRATION=true admits a new organisation without a code, and the sign-up page does not ask for one. Clear the code, or turn open registration off to require it")
	}
	return advisories
}

// CanVerifyEmail reports whether this install can deliver a confirmation
// link: mail configured and an origin to build the link on. It is the
// condition the verification service is enabled under.
func (c *Config) CanVerifyEmail() bool {
	return c.Email.Configured() && strings.TrimSpace(c.Server.AppBaseURL) != ""
}

// OpenRegistrationPendingWindow is OpenRegistrationPendingHours as a
// duration, floored at an hour for a config built without the loader.
func (s ServerConfig) OpenRegistrationPendingWindow() time.Duration {
	if s.OpenRegistrationPendingHours <= 0 {
		return time.Hour
	}
	return time.Duration(s.OpenRegistrationPendingHours) * time.Hour
}

// SecretsConfig holds settings for encrypting stored secrets at rest.
type SecretsConfig struct {
	// EncryptionKey is the key used to AES-256-GCM encrypt secret values. Any
	// length is accepted (it is hashed to 32 bytes). If empty, an insecure fixed
	// development key is used and a warning is logged.
	EncryptionKey string `yaml:"encryption_key"`
	// OldEncryptionKeys are previous keys accepted for DECRYPTION only, during a
	// key rotation (comma-separated in SECRETS_ENCRYPTION_KEY_OLD). Data is
	// re-encrypted to EncryptionKey by the rewrap job, after which they can be
	// removed.
	OldEncryptionKeys []string `yaml:"old_encryption_keys"`
	// Rewrap, when true, runs a one-shot re-encryption of every encrypted column
	// under the primary key at startup (SECRETS_ENCRYPTION_REWRAP=true), then
	// should be unset. Idempotent: rows already on the primary key are skipped.
	Rewrap bool `yaml:"rewrap"`
	// CDNSigningKey is the base secret for the HMAC capabilities on /cdn URLs,
	// image downloads and the per-serial EEPROM package (CDN_SIGNING_KEY).
	//
	// Empty falls back to the JWT secret, which is what shipped: one derived
	// key covered every tenant's signed URLs, and rotating it meant rotating
	// the JWT secret — i.e. signing out every user to re-key a download link.
	// Setting this separates the two blast radii and makes the CDN key
	// rotatable on its own. What a rotation costs is bounded and known: every
	// capability already minted stops verifying, and they are all short-lived
	// (15 minutes for the EEPROM package), so the window is minutes rather
	// than a re-flash — nothing is baked into a device.
	CDNSigningKey string `yaml:"cdn_signing_key"`
}

// ProvisioningConfig holds settings for generating device provisioning bundles
type ProvisioningConfig struct {
	// SDPayloadDir is the directory holding the extracted boot-partition contents
	// (config.txt, cmdline.txt, kernels, DTBs, overlays/, rootfs.cpio.gz, ...) that
	// are zipped together with a generated node-adoption.txt into the SD/USB
	// provisioning bundle. If empty, the SD provisioning endpoint is disabled.
	SDPayloadDir string `yaml:"sd_payload_dir"`

	// HTTPBootDir is the directory holding the signed bootstrap boot files
	// (boot.img, boot.sig) served by the network-first HTTP-boot endpoint
	// (GET /http-boot/:serial/:file). The endpoint decides per device whether to
	// serve these (netboot the bootstrap) or return 404 (fall through to NVMe/local
	// OS) based on the effective boot_to_os policy. If empty, HTTP-boot is disabled.
	HTTPBootDir string `yaml:"http_boot_dir"`

	// SourceOfferDir is the directory holding the corresponding-source bundle
	// for the recovery image this install serves (GPLv2 section 3: source
	// offered next to the object code), published tokenless at GET /source/.
	// Empty means the install publishes no offer and the route answers 404 —
	// right for an install whose devices are all its own, since the obligation
	// is to whoever RECEIVES the image. The files are placed by hand; the
	// server never writes here.
	SourceOfferDir string `yaml:"source_offer_dir"`

	// BootHost is the netboot HTTP_HOST this install bakes into device EEPROMs
	// and serves boot files from (e.g. "boot.orchestrator.sh"). When set, the
	// platform compares it to each node's reported EEPROM HTTP_HOST: a device
	// whose EEPROM points elsewhere is on a different install, so
	// netboot-recovery is blocked for it (the recovery cycle would never
	// complete against a foreign boot server). Empty disables the check.
	// env: PROVISIONING_BOOT_HOST
	BootHost string `yaml:"boot_host"`

	// HTTPBootFailOpen makes the HTTP-boot endpoint SERVE the bootstrap for an
	// unknown serial or on a lookup/policy error (instead of 404). This is
	// required ONLY for the on-prem pre-adoption flow, where a brand-new device
	// netboots the bootstrap to get adopted BEFORE the platform knows it. On a
	// internet-facing endpoint fail-open would hand the signed bootstrap to any Pi
	// on the internet and leak deployment topology, so the SAFE DEFAULT is
	// fail-CLOSED (false): only a known node with a non-boot-to-os policy is
	// served. env: PROVISIONING_HTTP_BOOT_FAIL_OPEN
	HTTPBootFailOpen bool `yaml:"http_boot_fail_open"`

	// BootFailsafeThreshold is the global default number of consecutive WITHHELD
	// netboot attempts (a crash-loop, since a healthy node boots its OS and its
	// agent reconnects) after which the endpoint SERVES the recovery bootstrap so
	// the node becomes inspectable, instead of withholding forever. 0 disables the
	// failsafe. A per-node override (Node.BootFailsafeThreshold) takes precedence.
	// SAFE DEFAULT is 0 (opt-in per install) — serving recovery is a safety-critical
	// decision and the counting endpoint is unauthenticated.
	// env: PROVISIONING_BOOT_FAILSAFE_THRESHOLD
	BootFailsafeThreshold int `yaml:"boot_failsafe_threshold"`
}

// ServerConfig holds server configuration
type ServerConfig struct {
	Port int    `yaml:"port"`
	Host string `yaml:"host"`
	// Environment selects the run mode. "production" turns on fail-closed
	// validation (see Config.Validate); anything else is treated as development.
	Environment      string          `yaml:"environment"`
	AdvertisementURL string          `yaml:"advertisement_url"` // Optional: URL to advertise in adoption files (e.g., "https://orchestrator.example.com:8080"). Must include port if not default. If empty, uses request host.
	ReadTimeout      time.Duration   `yaml:"readTimeout"`
	WriteTimeout     time.Duration   `yaml:"writeTimeout"`
	CORS             CORSConfig      `yaml:"cors"`
	WebSocket        WebSocketConfig `yaml:"websocket"`
	InviteCode       string          `yaml:"invite_code"`
	// MaxAccounts caps how many accounts this instance will create through
	// self-registration (0 = unlimited).
	//
	// The invite code it backs up is ONE shared string with no expiry and no
	// usage count — a password to the front door that everyone who was ever
	// given it still holds, including people who have already used it. This is
	// the bound that the code itself does not have, and it is a ceiling on the
	// INSTANCE rather than a per-person allowance: N accounts is N times every
	// per-account quota, on the operator's bill.
	//
	// Counted in ACCOUNTS, not users: an account is what carries a storage
	// quota, and an invited teammate joins an existing one rather than creating
	// another, so inviting colleagues does not spend this budget.
	// env: SERVER_MAX_ACCOUNTS
	MaxAccounts int `yaml:"max_accounts"`
	// OpenRegistration lets any new organisation register without a code
	// once the first account exists. Off by default: an empty invite code
	// still means CLOSED. When on it takes precedence over InviteCode (the
	// code is not asked for), and MaxAccounts still bounds it; both
	// couplings are reported by Advisories.
	// env: SERVER_OPEN_REGISTRATION
	OpenRegistration bool `yaml:"open_registration"`
	// OpenRegistrationMaxPending caps open sign-ups that are still waiting
	// for their owner to confirm the address. Such an account is usable for
	// nothing and does not count toward MaxAccounts, so this is what bounds
	// them; past it an open sign-up answers "try again later". Armed by
	// default and never unlimited: a zero, negative or unparseable value
	// leaves the default.
	// env: SERVER_OPEN_REGISTRATION_MAX_PENDING
	OpenRegistrationMaxPending int `yaml:"open_registration_max_pending"`
	// OpenRegistrationPendingHours is how long an unconfirmed open sign-up
	// counts toward OpenRegistrationMaxPending. Older ones stop holding the
	// cap, so abandoned sign-ups cannot keep it shut; they stay unusable.
	// env: SERVER_OPEN_REGISTRATION_PENDING_HOURS
	OpenRegistrationPendingHours int `yaml:"open_registration_pending_hours"`
	// AllowInsecureDevSecrets lets an install that has ALREADY opted out of
	// the hardened mode (APP_ENV=development|dev|local|test) boot on a weak,
	// placeholder or empty JWT_SECRET_KEY, SECRETS_ENCRYPTION_KEY or
	// CDN_SIGNING_KEY. Without it those three checks refuse the boot in
	// EVERY environment: a copied example file with its placeholders used to
	// run in development, signing tokens with a key printed in this
	// repository, and the only thing standing between that and a reachable
	// server was one APP_ENV line. In production the flag is inert and an
	// advisory says so — it is a development convenience, never a bypass.
	// env: ALLOW_INSECURE_DEV_SECRETS
	AllowInsecureDevSecrets bool `yaml:"allow_insecure_dev_secrets"`
	// WebAuthnRPID pins the WebAuthn relying-party id (a bare domain, no port).
	// Empty derives it from each request's Host, which is what allows one
	// binary to serve any number of hostnames — a
	// passkey is bound to its RP ID forever, so this must match the origin the
	// browser used. Set it only when a proxy rewrites Host.
	// env: WEBAUTHN_RP_ID
	WebAuthnRPID string `yaml:"webauthn_rp_id"`
	// AppBaseURL is the public origin of the SPA (e.g. "https://app.example.com"),
	// used to build absolute links that leave the system — today the email
	// verification link. Distinct from AdvertisementURL, which is what DEVICES
	// are told to call. env: APP_BASE_URL
	AppBaseURL string `yaml:"app_base_url"`
}

// WebSocketConfig pins which origins may open a WebSocket (the account fan-out
// socket and the node terminal).
//
// Separate from CORS on purpose: CORS governs XHR/fetch, where the browser
// enforces the response header, while a WebSocket handshake is exempt from the
// same-origin policy entirely — CheckOrigin is the only thing between a foreign
// page and an authenticated socket. Tying the two together is what made the
// upgrader allow-all whenever CORS was "enabled", which is always.
//
// Empty means "use the CORS origins, and same-origin if those say nothing" —
// see infrastructure/http_server/wsorigin. env: WEBSOCKET_ALLOWED_ORIGINS
type WebSocketConfig struct {
	AllowedOrigins []string `yaml:"allowedOrigins"`
}

// CORSConfig holds CORS configuration
type CORSConfig struct {
	Enabled          bool     `yaml:"enabled"`
	AllowedOrigins   []string `yaml:"allowedOrigins"`
	AllowedMethods   []string `yaml:"allowedMethods"`
	AllowedHeaders   []string `yaml:"allowedHeaders"`
	AllowCredentials bool     `yaml:"allowCredentials"`
	MaxAge           int      `yaml:"maxAge"`
}

// DatabaseConfig holds database configuration
type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
	// ProductionLogging suppresses GORM's per-statement query log (which
	// interpolates parameter VALUES into the log, including anything read from
	// a credential column). Derived from Server.Environment, not configured
	// directly — see Config.applyDerived.
	ProductionLogging bool `yaml:"-"`
}

// JWTConfig holds JWT authentication configuration
type JWTConfig struct {
	SecretKey       string        `yaml:"secret_key"`
	AccessTokenTTL  time.Duration `yaml:"access_token_ttl"`
	RefreshTokenTTL time.Duration `yaml:"refresh_token_ttl"`
	Issuer          string        `yaml:"issuer"`
	// MaxSessionsPerUser caps concurrent logins per user. A fresh login past
	// the cap revokes the least recently active session rather than being
	// refused — a cap that blocks sign-in turns a forgotten session into a
	// lockout. 0 is unlimited, the polarity the other quotas use.
	// env: AUTH_MAX_SESSIONS_PER_USER
	MaxSessionsPerUser int `yaml:"max_sessions_per_user"`
}

// OIDCConfig holds generic OIDC (Google/Azure/Okta/…) login configuration.
// Google is just IssuerURL = https://accounts.google.com. When Enabled is
// false the login endpoints return a "not configured" error and no provider is
// initialized.
type OIDCConfig struct {
	Enabled      bool   `yaml:"enabled"`
	IssuerURL    string `yaml:"issuer_url"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	// RedirectURL is the backend callback registered with the IdP.
	RedirectURL string `yaml:"redirect_url"`
	// AutoProvision, when true, creates a new user + account for any verified
	// email with no existing user (SaaS). When false (default), an unknown
	// verified email is rejected — the person must be invited first
	// (enterprise/self-hosted safe default).
	AutoProvision bool `yaml:"auto_provision"`
	// ProviderName is the identity provider's name as people know it
	// ("Google", "Keycloak", the company's own sign-in). The SPA puts it on
	// the sign-in button and the account page. Empty derives it from the
	// issuer: Google's issuer names itself, any other gets no name and the
	// SPA says "single sign-on". env: OIDC_PROVIDER_NAME
	ProviderName string `yaml:"provider_name"`
}

// isGoogleIssuer reports whether the issuer is Google's.
func (o OIDCConfig) isGoogleIssuer() bool {
	return strings.Contains(o.IssuerURL, "accounts.google.com")
}

// DisplayName is the provider name the SPA shows: the configured name, else
// "Google" for Google's issuer, else empty (the SPA's generic wording).
func (o OIDCConfig) DisplayName() string {
	if name := strings.TrimSpace(o.ProviderName); name != "" {
		return name
	}
	if o.isGoogleIssuer() {
		return "Google"
	}
	return ""
}

// ButtonLabel returns the legacy English login-button label. The SPA builds
// its own translated label from DisplayName; this stays for clients that
// predate that field.
func (o OIDCConfig) ButtonLabel() string {
	if name := o.DisplayName(); name != "" {
		return "Sign in with " + name
	}
	return "Sign in with SSO"
}

// GRPCConfig holds gRPC server configuration
type GRPCConfig struct {
	Port             int    `yaml:"port"`
	Host             string `yaml:"host"`
	AdvertisementURL string `yaml:"advertisement_url"` // Optional: Endpoint to advertise in adoption files (e.g., "orchestrator.example.com:50051"). Must include port. If empty, uses host:port.
	// TLS for the agent gRPC endpoint. When both are set the server serves TLS
	// (required for a public SaaS endpoint); when empty it serves plaintext
	// (acceptable inside a trusted self-hosted LAN).
	TLSCertFile string `yaml:"tls_cert_file"`
	TLSKeyFile  string `yaml:"tls_key_file"`
	// TLSTerminatedExternally acknowledges that TLS for the agent endpoint is
	// terminated by an upstream proxy (e.g. Traefik with Let's Encrypt) and the
	// backend only ever receives plaintext gRPC over a trusted internal network.
	// Setting this satisfies production validation without backend cert files.
	TLSTerminatedExternally bool `yaml:"tls_terminated_externally"`
	// PlaintextOK declares that the agent endpoint is served in plaintext ON
	// PURPOSE — a trusted LAN with no reverse proxy and no certificate, where
	// the devices reach this host over an http:// platform URL. It is the
	// third supported shape beside certificate files and external
	// termination, and the one a first install with no proxy needs: without
	// a declaration production refuses the boot naming the shapes, with it
	// the boot proceeds under ONE advisory that says what crosses that wire.
	// Inert whenever the wire is TLS anyway.
	// env: AGENT_GRPC_PLAINTEXT_OK
	PlaintextOK bool `yaml:"plaintext_ok"`
	// ProxyProtocol says whether the listener expects a HAProxy PROXY protocol
	// header (v1 or v2) in front of every connection, so that the backend sees
	// the REAL client address instead of the upstream proxy's. Exactly two
	// values — GRPCProxyProtocolOff and GRPCProxyProtocolRequire; see the
	// constants for why there is deliberately no "optional".
	// env: AGENT_GRPC_PROXY_PROTOCOL
	ProxyProtocol string `yaml:"proxy_protocol"`
}

// The PROXY protocol modes for the agent gRPC listener.
//
// Behind a TCP proxy that forwards WITHOUT a PROXY header, `peer.FromContext`
// in the gRPC handler answers the proxy's own address for every agent. Every
// per-client bound then has to live at the edge, and the backend's pre-auth
// gate can only be global. With `require`, the proxy (Traefik:
// `proxyProtocol.version: 2` on the TCP service) prepends the client's address
// to each connection and the backend keys its per-source pre-auth gate on it.
//
// There is deliberately NO "optional" mode (go-proxyproto's USE policy: honour
// a header when present, accept the connection without one). Optional is the
// unsafe shape: a peer that can reach a listener whose upstream does NOT
// prepend a header — a direct connection on an install that has not enabled
// it at the proxy, or a misrouted one — could write its own header naming a
// victim's address, and every stream it then opens is counted against THAT
// address's per-source bucket. The victim site's devices are refused while the
// attacker's own address never appears. `require` closes that: the header
// always comes from the upstream proxy, and a client-written header arrives
// AFTER it as payload, where it merely breaks that client's own handshake.
// `off` installs no wrapper at all and keeps the per-source gate inert, since
// without a real address a per-source key would BE the fleet-wide refusal it
// exists to prevent.
const (
	// GRPCProxyProtocolOff — plain listener, proxy address seen as the peer,
	// per-source pre-auth gate inert. The default, so an install that has not
	// configured its proxy keeps working.
	GRPCProxyProtocolOff = "off"
	// GRPCProxyProtocolRequire — every connection MUST open with a PROXY
	// header; one without is refused at the first read. Set together with the
	// proxy-side change, in the same deploy.
	GRPCProxyProtocolRequire = "require"
)

// GRPCProxyProtocolModes lists the accepted values in the order an error
// message should name them.
var GRPCProxyProtocolModes = []string{GRPCProxyProtocolOff, GRPCProxyProtocolRequire}

// ProxyProtocolProblem reports the fail-closed refusal for an unrecognised
// mode, or "" when the value is accepted. Empty is accepted and means off (the
// YAML default), because most installs never set it.
//
// It is a method rather than an inline check so that BOTH consumers — Problems()
// at config load and the gRPC listener at Start — ask the same question: a
// typo such as "required" must not quietly select "off" on an install that
// believes it is bounded per source.
func (g GRPCConfig) ProxyProtocolProblem() string {
	v := strings.TrimSpace(g.ProxyProtocol)
	if v == "" {
		return ""
	}
	for _, mode := range GRPCProxyProtocolModes {
		if v == mode {
			return ""
		}
	}
	return fmt.Sprintf("AGENT_GRPC_PROXY_PROTOCOL is %q, which is not one of %s; an unrecognised value is refused rather than read as \"off\"",
		v, strings.Join(GRPCProxyProtocolModes, "|"))
}

// TLSDirect reports whether the server itself terminates TLS on the agent
// listener — both certificate files set. ONE definition for Problems(), the
// advisory and the listener, so the three never disagree about what "TLS at
// the server" means.
func (g GRPCConfig) TLSDirect() bool {
	return g.TLSCertFile != "" && g.TLSKeyFile != ""
}

// PlaintextByDeclaration reports whether the wire to the devices is plaintext
// and the install has said so (AGENT_GRPC_PLAINTEXT_OK with neither
// certificate files nor external termination) — the case the advisory
// reports and the listener logs as deliberate.
func (g GRPCConfig) PlaintextByDeclaration() bool {
	return g.PlaintextOK && !g.TLSDirect() && !g.TLSTerminatedExternally
}

// ProxyProtocolRequired reports whether the listener must be wrapped and the
// per-source pre-auth gate armed.
func (g GRPCConfig) ProxyProtocolRequired() bool {
	return strings.TrimSpace(g.ProxyProtocol) == GRPCProxyProtocolRequire
}

// StorageConfig holds storage configuration
type StorageConfig struct {
	Type             string             `yaml:"type"` // Storage type: "gcp", "s3", "local", or "auto" (default)
	GCP              GCPStorageConfig   `yaml:"gcp"`
	S3               S3StorageConfig    `yaml:"s3"`
	Local            LocalStorageConfig `yaml:"local"`
	MaxFileSize      int64              `yaml:"max_file_size"`       // Maximum file size in bytes
	AgentCDNBasePath string             `yaml:"agent_cdn_base_path"` // Base path (within storage namespace) for agent CDN artifacts

	// TmpSweepHours is how long an abandoned multipart spill file may sit in
	// TMPDIR before the sweeper deletes it. 0 disables sweeping.
	//
	// The default is deliberately generous. TMPDIR is on the storage volume and
	// a spill is the size of the upload, so an orphan is expensive — but the
	// server sets no blanket ReadTimeout precisely so that a multi-gigabyte
	// image upload can outlive any fixed deadline, and reaping a live one would
	// fail the upload (the temp file is reopened by path once the body is in).
	// Six hours is longer than a realistic multi-gigabyte upload and far
	// shorter than the window in which a 5 GiB orphan matters.
	// env: STORAGE_TMP_SWEEP_HOURS
	TmpSweepHours int `yaml:"tmp_sweep_hours"`
}

// GCPStorageConfig holds GCP storage configuration
type GCPStorageConfig struct {
	BucketName      string `yaml:"bucket_name"`
	ProjectID       string `yaml:"project_id"`
	Credentials     string `yaml:"credentials"`      // Base64 encoded service account JSON
	CredentialsFile string `yaml:"credentials_file"` // Alternative: path to credentials file
}

// S3StorageConfig holds S3-compatible object storage configuration. Any
// S3-compatible store works; nothing here is AWS-specific.
//
// The credentials are read through secretEnv, so `S3_ACCESS_KEY_FILE` /
// `S3_SECRET_KEY_FILE` point at mounted Docker secrets and the plain env vars
// remain a fallback.
type S3StorageConfig struct {
	// Endpoint is the service URL, e.g. https://s3.region.example.com. It is
	// the LOCATION endpoint, not the bucket URL — the bucket name is a separate
	// field and the SDK prepends it (virtual-hosted style).
	Endpoint string `yaml:"endpoint"`
	// Region is the location code the provider expects.
	Region string `yaml:"region"`
	Bucket string `yaml:"bucket"`
	// AccessKey/SecretKey come from S3_ACCESS_KEY / S3_SECRET_KEY (or their
	// _FILE variants). They are never written back to YAML.
	AccessKey string `yaml:"-"`
	SecretKey string `yaml:"-"`
	// UsePathStyle switches to https://endpoint/bucket/key addressing. Hosted
	// providers mostly serve virtual-hosted style (<bucket>.<endpoint>), so
	// this stays off for them; it exists because MinIO and several on-prem
	// gateways need it.
	UsePathStyle bool `yaml:"use_path_style"`
	// PartSizeBytes bounds uploader memory together with UploadConcurrency:
	// an upload holds up to concurrency × part size in RAM. Defaults to 16 MiB
	// × 3 = 48 MiB, which is deliberate on a small host that also runs Postgres.
	PartSizeBytes     int64 `yaml:"part_size_bytes"`
	UploadConcurrency int   `yaml:"upload_concurrency"`
}

// LocalStorageConfig holds local filesystem storage configuration
type LocalStorageConfig struct {
	BaseDir string `yaml:"base_dir"` // Base directory for file storage
	BaseURL string `yaml:"base_url"` // Base URL for generating file access URLs
}

// LoadConfig loads configuration from YAML file
func LoadConfig(r io.Reader) (*Config, error) {
	cfg := getDefaultConfig()

	decoder := yaml.NewDecoder(r)
	err := decoder.Decode(cfg)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

// Load loads configuration from environment variables
func Load() (*Config, error) {
	cfg := getDefaultConfig()
	return ApplyEnvironment(cfg)
}

// getDefaultConfig returns a Config with default values
func getDefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port: 8080,
			Host: "0.0.0.0",
			// Deliberately EMPTY, not "development": an install that never
			// states its environment must land in the hardened mode, not the
			// loose one. See IsProduction.
			Environment:  "",
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			// Open-registration bounds, armed whether or not open
			// registration is on (inert while it is off).
			OpenRegistrationMaxPending:   20,
			OpenRegistrationPendingHours: 48,
			CORS: CORSConfig{
				Enabled:          true,
				AllowedOrigins:   []string{"*"},
				AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
				AllowedHeaders:   []string{"Origin", "Content-Type", "Accept", "Authorization"},
				AllowCredentials: true,
				MaxAge:           86400, // 24 hours
			},
		},
		Database: DatabaseConfig{
			Host:     "localhost",
			Port:     5432,
			User:     "postgres",
			Password: "postgres",
			DBName:   "orchestrator_platform",
			SSLMode:  "disable",
		},
		Terminal: TerminalConfig{
			// Same shape as the login-session cap's default; enough for any
			// real multi-pane debugging, far below a leak.
			MaxSessionsPerUser:        5,
			MaxPendingHandshakes:      DefaultTerminalMaxPendingHandshakes,
			MaxPendingHandshakesPerIP: DefaultTerminalMaxPendingHandshakesPerIP,
		},
		JWT: JWTConfig{
			SecretKey: insecureDefaultJWTSecret,
			// Short-lived access tokens bound the window a revoked member
			// keeps access; the refresh flow re-checks membership.
			AccessTokenTTL:  15 * time.Minute,
			RefreshTokenTTL: 7 * 24 * time.Hour,
			Issuer:          "orchestrator",
			// Enough for a laptop, a desktop, a phone and a few stale ones;
			// low enough that sessions cannot accumulate without bound.
			MaxSessionsPerUser: 10,
		},
		GRPC: GRPCConfig{
			Port:          50051,
			Host:          "0.0.0.0",
			ProxyProtocol: GRPCProxyProtocolOff,
		},
		Storage: StorageConfig{
			Type: "auto", // Auto-detect: use GCP if configured, otherwise local
			GCP: GCPStorageConfig{
				BucketName:      "",
				ProjectID:       "",
				Credentials:     "",
				CredentialsFile: "",
			},
			Local: LocalStorageConfig{
				BaseDir: "./storage",
				BaseURL: "/cdn",
			},
			// Ceiling for a single uploaded file (OS images are the largest —
			// several GB). Non-zero so the size check is active by default;
			// override with STORAGE_MAX_FILE_SIZE. 0 disables the check.
			MaxFileSize:      16 << 30,          // 16 GiB
			AgentCDNBasePath: "agent-artifacts", // default relative base path for agent artifacts
			TmpSweepHours:    6,                 // reap abandoned multipart spills; 0 disables
		},
		Provisioning: ProvisioningConfig{
			SDPayloadDir:   "", // empty = SD provisioning endpoint disabled
			HTTPBootDir:    "", // empty = HTTP-boot (network-first) endpoint disabled
			SourceOfferDir: "", // empty = no corresponding-source offer at /source/
		},
		EEPROMSigner: EEPROMSignerConfig{
			URL: "", // empty = on-platform EEPROM provisioning disabled
		},
		Secrets: SecretsConfig{
			EncryptionKey: "", // empty = insecure dev fallback key (warns at startup)
		},
		SSHAdopt: SSHAdoptConfig{
			Enabled:                 false, // OFF unless explicitly enabled
			AllowedCDNBaseURL:       "",
			AdoptionFilePath:        "/etc/orchestrator/node-adoption.txt",
			HostKeyPolicy:           "tofu",
			ConnectTimeoutSeconds:   15,
			CommandTimeoutSeconds:   60,
			AgentWaitTimeoutSeconds: 300,
		},
		Workflow: WorkflowConfig{
			ExecutionRetentionDays:        30,
			MaxActiveExecutionsPerAccount: 20,
			MaxCronPlaybooksPerAccount:    10,
			// Half the 500-worker pool, far above any fleet this was sized
			// against: reaching 250 needs ~28 nodes in each of the 20 permitted
			// concurrent executions. It is a runaway guard, not a product limit,
			// and a bind is logged so the first tenant to meet it is visible
			// rather than silently refused.
			MaxRunningNodesPerAccount: 250,
		},
		// The values the tracked-files feature shipped with. Unlike the other
		// quota blocks these do not default to unlimited: unbounded file history
		// was never a chosen on-prem behaviour, only an unconfigurable one.
		// Kept in step with node_file.DefaultLimits(), which a test pins: the
		// config package stays free of domain imports, so the numbers are
		// repeated here rather than referenced.
		NodeFile: NodeFileConfig{
			MaxVersionsPerPath: 20,
			MaxFilesPerNode:    50,
			MaxBytesPerNode:    16 * 1024 * 1024,
			MaxBytesPerAccount: 0, // unlimited until an install decides otherwise
		},
		Member: MemberConfig{
			// Armed by default, unlike the storage quotas beside it. Those bound
			// what a tenant costs its own operator, which is that operator's call;
			// this one bounds mail sent to strangers over the operator's domain,
			// and an install that has not thought about it should still not be a
			// relay. 20 a day is far above any real team's onboarding.
			MaxInvitesPerDay: 20,
		},
		Image: ImageConfig{
			MaxUploadSize:             0, // no per-image cap beyond storage.max_file_size
			MaxPerAccount:             0, // unlimited
			MaxBytesPerAccount:        0, // unlimited
			MaxBytesInstance:          0, // unlimited
			ReplaceOriginalOnCompress: false,
			AllowPlaintextURL:         false,
			MaxDecompressedSize:       0, // derived from MaxUploadSize, else the default
			// Armed by default, unlike the ceilings above: this one does not
			// bound what a tenant may store, it makes the count cap the operator
			// already chose actually hold under concurrency. An install with no
			// count cap never reaches the query at all.
			CaptureInFlightWindowMinutes: 60,
			// Armed too, for the same reason: a bound on concurrent staging is
			// what keeps one tenant's uploads from filling the volume every
			// tenant spills to. 0 here would still mean the
			// default through EffectiveMaxConcurrentUploads*, never unlimited.
			MaxConcurrentUploads:           DefaultImageMaxConcurrentUploads,
			MaxConcurrentUploadsPerAccount: DefaultImageMaxConcurrentUploadsPerAccount,
			XZMemLimitMB:                   DefaultImageXZMemLimitMB,
			MaxConcurrentCompressions:      DefaultImageMaxConcurrentCompressions,
			MaxStagingBytes:                DefaultImageMaxStagingBytes,
			CompressionTimeoutMinutes:      DefaultImageCompressionTimeoutMinutes,
		},
		// Command history: an explicit zero, not an omission — a command
		// sweep that ran unasked would be a surprise on the filesystem that
		// breaks first. The cost of forever is real and named:
		// node_commands.payload/result/details are rewrap targets, so
		// unbounded growth slows every future key rotation. An operator who
		// wants a bound sets NODE_COMMAND_RETENTION_DAYS. The audit trail is
		// kept a year unless the install says otherwise (0 = forever).
		Retention: RetentionConfig{
			NodeCommandDays: 0,
			AuditEventDays:  DefaultAuditEventRetentionDays,
		},
		Quota: QuotaConfig{
			// 20 req/s sustained with a burst of 100, per ACCOUNT. Sized
			// against what this UI actually does rather than picked round: a
			// cold page load is tens of requests and the polling endpoints
			// (command scans) run at seconds, not milliseconds. A real operator
			// cannot reach this; a loop reaches it immediately.
			APIRequestsPerSecond: 20,
			APIBurst:             100,
			// The three counts are set well above any real account because
			// their job is to stop a runaway, not to price a plan. When this
			// becomes a commercial
			// limit it should be per-plan and stored, not a process-wide
			// constant — do not repurpose these.
			MaxNodesPerAccount: 500,
			// Thirty days: long enough for hardware ordered against a WebUSB
			// placeholder to arrive, short enough that a squat is a standing
			// cost rather than a one-off. The cheapest squat path is a plain
			// authenticated POST; this is what makes holding it expensive.
			SerialLeaseDays:      30,
			MaxMembersPerAccount: 100,
			MaxSecretsPerAccount: 5000,
			// Assignments are rows an operator creates by hand, so 500 is far
			// above any real account; the concurrency bound follows the
			// workflow pool's reasoning (250 = half the 500-worker pool).
			MaxUtilityAssignmentsPerAccount: 500,
			MaxConcurrentUtilityApplies:     250,
			// An honest agent holds ONE stream, transiently two while a
			// reconnect overlaps a teardown, and a stream whose peer vanished
			// is reaped by the transport keepalive within ~40 s. 32 is sixteen
			// times that ceiling: unreachable by reconnecting, reachable only
			// by a peer deliberately holding streams open. The inbound pace is
			// two orders of magnitude above what an agent sends in steady state
			// (a heartbeat every 30 s, a metrics sample every 5 s) and five
			// times the largest real burst, a reconnect replaying 20 queued
			// command results.
			MaxAgentStreamsPerNode:       32,
			AgentStreamMessagesPerSecond: 20,
			AgentStreamBurst:             100,
			// The global pre-auth gate; see grpc/preauth_limits.go for what it
			// bounds with and without PROXY protocol.
			MaxPreAuthStreams: 1024,
			// The backend's own per-address bound, independent of the edge's:
			// one address may hold at most 256 of the 1024 global slots once
			// PROXY protocol gives the backend a real address, so exhausting
			// the gate needs four addresses.
			MaxPreAuthStreamsPerSource: 256,
		},
	}
}

// ApplyEnvironment overrides config values with environment variables
func ApplyEnvironment(cfg *Config) (*Config, error) {
	// SERVER CONFIG
	if env := os.Getenv("APP_ENV"); env != "" {
		cfg.Server.Environment = env
	}

	if host := os.Getenv("SERVER_HOST"); host != "" {
		cfg.Server.Host = host
	}

	if port := os.Getenv("SERVER_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			cfg.Server.Port = p
		}
	}

	if advertisementURL := os.Getenv("SERVER_ADVERTISEMENT_URL"); advertisementURL != "" {
		cfg.Server.AdvertisementURL = advertisementURL
	}

	// DATABASE CONFIG
	if host := os.Getenv("DB_HOST"); host != "" {
		cfg.Database.Host = host
	}

	if port := os.Getenv("DB_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			cfg.Database.Port = p
		}
	}

	if user := os.Getenv("DB_USER"); user != "" {
		cfg.Database.User = user
	}

	if password := secretEnv("DB_PASSWORD"); password != "" {
		cfg.Database.Password = password
	}

	if dbName := os.Getenv("DB_NAME"); dbName != "" {
		cfg.Database.DBName = dbName
	}

	if sslMode := os.Getenv("DB_SSLMODE"); sslMode != "" {
		cfg.Database.SSLMode = sslMode
	}

	// JWT CONFIG
	if secretKey := secretEnv("JWT_SECRET_KEY"); secretKey != "" {
		cfg.JWT.SecretKey = secretKey
	}

	if accessTTL := os.Getenv("JWT_ACCESS_TOKEN_TTL"); accessTTL != "" {
		if duration, err := time.ParseDuration(accessTTL); err == nil {
			cfg.JWT.AccessTokenTTL = duration
		}
	}

	if refreshTTL := os.Getenv("JWT_REFRESH_TOKEN_TTL"); refreshTTL != "" {
		if duration, err := time.ParseDuration(refreshTTL); err == nil {
			cfg.JWT.RefreshTokenTTL = duration
		}
	}

	if issuer := os.Getenv("JWT_ISSUER"); issuer != "" {
		cfg.JWT.Issuer = issuer
	}

	// OIDC CONFIG
	if enabled := os.Getenv("OIDC_ENABLED"); enabled != "" {
		if e, err := strconv.ParseBool(enabled); err == nil {
			cfg.OIDC.Enabled = e
		}
	}
	if v := os.Getenv("OIDC_ISSUER_URL"); v != "" {
		cfg.OIDC.IssuerURL = v
	}
	if v := os.Getenv("OIDC_CLIENT_ID"); v != "" {
		cfg.OIDC.ClientID = v
	}
	if v := secretEnv("OIDC_CLIENT_SECRET"); v != "" {
		cfg.OIDC.ClientSecret = v
	}
	if v := os.Getenv("OIDC_REDIRECT_URL"); v != "" {
		cfg.OIDC.RedirectURL = v
	}
	if v := os.Getenv("OIDC_AUTO_PROVISION"); v != "" {
		if e, err := strconv.ParseBool(v); err == nil {
			cfg.OIDC.AutoProvision = e
		}
	}
	if v := os.Getenv("OIDC_PROVIDER_NAME"); v != "" {
		cfg.OIDC.ProviderName = v
	}

	// EMAIL CONFIG
	if v := os.Getenv("EMAIL_ENABLED"); v != "" {
		if e, err := strconv.ParseBool(v); err == nil {
			cfg.Email.Enabled = e
		}
	}
	if v := os.Getenv("EMAIL_SMTP_HOST"); v != "" {
		cfg.Email.Host = v
	}
	if v := os.Getenv("EMAIL_SMTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Email.Port = n
		}
	}
	if v := os.Getenv("EMAIL_SMTP_USERNAME"); v != "" {
		cfg.Email.Username = v
	}
	if v := secretEnv("EMAIL_SMTP_PASSWORD"); v != "" {
		cfg.Email.Password = v
	}
	if v := os.Getenv("EMAIL_FROM_ADDRESS"); v != "" {
		cfg.Email.FromAddress = v
	}
	if v := os.Getenv("EMAIL_FROM_NAME"); v != "" {
		cfg.Email.FromName = v
	}
	if v := os.Getenv("EMAIL_SMTP_STARTTLS"); v != "" {
		if e, err := strconv.ParseBool(v); err == nil {
			cfg.Email.StartTLS = e
		}
	}
	if v := os.Getenv("APP_BASE_URL"); v != "" {
		cfg.Server.AppBaseURL = v
	}

	// SERVER INVITE CODE
	if inviteCode := secretEnv("SERVER_INVITE_CODE"); inviteCode != "" {
		cfg.Server.InviteCode = inviteCode
	}
	if v := os.Getenv("SERVER_MAX_ACCOUNTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Server.MaxAccounts = n
		}
	}
	// An unparseable value does NOT open sign-up: opening it must be asked
	// for in a spelling the loader recognises.
	if v := os.Getenv("SERVER_OPEN_REGISTRATION"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Server.OpenRegistration = b
		}
	}
	// Neither bound has an "unlimited": a value that cannot bound is ignored.
	if v := os.Getenv("SERVER_OPEN_REGISTRATION_MAX_PENDING"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Server.OpenRegistrationMaxPending = n
		}
	}
	if v := os.Getenv("SERVER_OPEN_REGISTRATION_PENDING_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Server.OpenRegistrationPendingHours = n
		}
	}
	// An unparseable value does NOT opt in: the loose mode must be asked for
	// in a spelling the loader recognises, like APP_ENV itself.
	if v := os.Getenv("ALLOW_INSECURE_DEV_SECRETS"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Server.AllowInsecureDevSecrets = b
		}
	}

	// DEVICE ENTITLEMENT. The file is read with the credential discipline
	// (ENTITLEMENT_FILE first) because it is seeded beside the secrets, not
	// because it is one: it grants a cap, nothing more.
	if doc := secretEnv("ENTITLEMENT"); doc != "" {
		cfg.Entitlement.Document = doc
	}
	if v := os.Getenv("ENTITLEMENT_SUBSCRIPTION_URL"); v != "" {
		cfg.Entitlement.SubscriptionURL = v
	}
	// The per-account device limit. A value that is not a non-negative
	// integer is kept as -1, so Advisories names it; admission then applies
	// no per-account limit, which is what an unset value means too.
	if v := os.Getenv("ACCOUNT_DEVICE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			cfg.Entitlement.AccountDeviceLimit = n
		} else {
			cfg.Entitlement.AccountDeviceLimit = -1
		}
	}
	if v := os.Getenv("ACCOUNT_DEVICE_LIMIT_CONTACT"); v != "" {
		cfg.Entitlement.AccountDeviceLimitContact = v
	}

	// CORS allowed origins (comma-separated). Pin this to the app origin(s) in
	// production instead of the wildcard default.
	if origins := os.Getenv("CORS_ALLOWED_ORIGINS"); strings.TrimSpace(origins) != "" {
		var list []string
		for _, o := range strings.Split(origins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				list = append(list, o)
			}
		}
		if len(list) > 0 {
			cfg.Server.CORS.AllowedOrigins = list
		}
	}

	// Concurrent sessions per user. 0 disables the cap.
	if raw := strings.TrimSpace(os.Getenv("AUTH_MAX_SESSIONS_PER_USER")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			cfg.JWT.MaxSessionsPerUser = n
		}
	}

	// WebSocket allowed origins (comma-separated). Optional: unset means the
	// CORS origins, and same-origin when those name nothing usable. Set it when
	// the socket origins must differ from the API's CORS policy.
	if origins := os.Getenv("WEBSOCKET_ALLOWED_ORIGINS"); strings.TrimSpace(origins) != "" {
		var list []string
		for _, o := range strings.Split(origins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				list = append(list, o)
			}
		}
		if len(list) > 0 {
			cfg.Server.WebSocket.AllowedOrigins = list
		}
	}

	// TERMINAL (web-SSH) — off by default; enable explicitly.
	if enabled := os.Getenv("TERMINAL_ENABLED"); enabled != "" {
		if e, err := strconv.ParseBool(enabled); err == nil {
			cfg.Terminal.Enabled = e
		}
	}
	if raw := strings.TrimSpace(os.Getenv("TERMINAL_MAX_SESSIONS_PER_USER")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			cfg.Terminal.MaxSessionsPerUser = n
		}
	}
	// Pre-auth handshake admission, global and per client address. Unlike the
	// session cap there is no zero-means-unlimited: a value that cannot bound
	// (0, negative, unparseable) leaves the configured cap alone, the same
	// rule as AGENT_MAX_PREAUTH_STREAMS.
	if raw := strings.TrimSpace(os.Getenv("TERMINAL_MAX_PENDING_HANDSHAKES")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			cfg.Terminal.MaxPendingHandshakes = n
		}
	}
	if raw := strings.TrimSpace(os.Getenv("TERMINAL_MAX_PENDING_HANDSHAKES_PER_IP")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			cfg.Terminal.MaxPendingHandshakesPerIP = n
		}
	}

	// SSH ADOPT — outbound SSH device onboarding; off by default.
	if enabled := os.Getenv("SSH_ADOPT_ENABLED"); enabled != "" {
		if e, err := strconv.ParseBool(enabled); err == nil {
			cfg.SSHAdopt.Enabled = e
		}
	}
	if v := os.Getenv("SSH_ADOPT_CDN_BASE_URL"); v != "" {
		cfg.SSHAdopt.AllowedCDNBaseURL = v
	}
	if v := os.Getenv("SSH_ADOPT_ADOPTION_FILE_PATH"); v != "" {
		cfg.SSHAdopt.AdoptionFilePath = v
	}
	if v := os.Getenv("SSH_ADOPT_HOST_KEY_POLICY"); v != "" {
		cfg.SSHAdopt.HostKeyPolicy = v
	}
	if v := os.Getenv("SSH_ADOPT_ALLOW_AGENT_AUTH"); v != "" {
		if e, err := strconv.ParseBool(v); err == nil {
			cfg.SSHAdopt.AllowAgentAuth = e
		}
	}
	if v := os.Getenv("SSH_ADOPT_AGENT_SOCK_DIR"); v != "" {
		cfg.SSHAdopt.AgentSockDir = v
	}
	if v := os.Getenv("SSH_ADOPT_ALLOWED_HOST_CIDRS"); v != "" {
		parts := strings.Split(v, ",")
		cidrs := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				cidrs = append(cidrs, p)
			}
		}
		cfg.SSHAdopt.AllowedHostCIDRs = cidrs
	}

	// GRPC CONFIG
	if host := os.Getenv("GRPC_HOST"); host != "" {
		cfg.GRPC.Host = host
	}

	if port := os.Getenv("GRPC_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			cfg.GRPC.Port = p
		}
	}

	if advertisementURL := os.Getenv("GRPC_ADVERTISEMENT_URL"); advertisementURL != "" {
		cfg.GRPC.AdvertisementURL = advertisementURL
	}

	if certFile := os.Getenv("GRPC_TLS_CERT_FILE"); certFile != "" {
		cfg.GRPC.TLSCertFile = certFile
	}

	if keyFile := os.Getenv("GRPC_TLS_KEY_FILE"); keyFile != "" {
		cfg.GRPC.TLSKeyFile = keyFile
	}

	if v := os.Getenv("GRPC_TLS_TERMINATED_EXTERNALLY"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.GRPC.TLSTerminatedExternally = b
		}
	}
	if v := os.Getenv("AGENT_GRPC_PLAINTEXT_OK"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.GRPC.PlaintextOK = b
		}
	}

	// Taken verbatim, NOT normalised or validated here: Problems() refuses an
	// unrecognised value with a message naming it, and the gRPC listener
	// refuses to start on one. Coercing "required" to "off" at this point
	// would hide exactly the mistake those two checks exist to catch.
	if v := os.Getenv("AGENT_GRPC_PROXY_PROTOCOL"); v != "" {
		cfg.GRPC.ProxyProtocol = v
	}

	// STORAGE CONFIG
	if storageType := os.Getenv("STORAGE_TYPE"); storageType != "" {
		cfg.Storage.Type = storageType
	}

	if bucketName := os.Getenv("GCP_STORAGE_BUCKET"); bucketName != "" {
		cfg.Storage.GCP.BucketName = bucketName
	}

	if projectID := os.Getenv("GCP_PROJECT_ID"); projectID != "" {
		cfg.Storage.GCP.ProjectID = projectID
	}

	if credentials := os.Getenv("GCP_CREDENTIALS"); credentials != "" {
		cfg.Storage.GCP.Credentials = credentials
	}

	if credentialsFile := os.Getenv("GCP_CREDENTIALS_FILE"); credentialsFile != "" {
		cfg.Storage.GCP.CredentialsFile = credentialsFile
	}

	if baseDir := os.Getenv("STORAGE_LOCAL_BASE_DIR"); baseDir != "" {
		cfg.Storage.Local.BaseDir = baseDir
	}

	if baseURL := os.Getenv("STORAGE_LOCAL_BASE_URL"); baseURL != "" {
		cfg.Storage.Local.BaseURL = baseURL
	}

	if maxFileSize := os.Getenv("STORAGE_MAX_FILE_SIZE"); maxFileSize != "" {
		if size, err := strconv.ParseInt(maxFileSize, 10, 64); err == nil {
			cfg.Storage.MaxFileSize = size
		}
	}

	// Note the >= 0: unlike the size limits above, 0 is a meaningful value here
	// (sweeping off), so an explicit "0" must be able to override the default.
	if sweep := os.Getenv("STORAGE_TMP_SWEEP_HOURS"); sweep != "" {
		if hours, err := strconv.Atoi(sweep); err == nil && hours >= 0 {
			cfg.Storage.TmpSweepHours = hours
		}
	}

	// INSTANCE MONITORING — who reads what the detectors record.
	if v := os.Getenv("INSTANCE_MONITORING_READER"); v != "" {
		cfg.Monitoring.AnomalyReader = v
	}

	// S3-COMPATIBLE OBJECT STORAGE
	if v := os.Getenv("S3_ENDPOINT"); v != "" {
		cfg.Storage.S3.Endpoint = v
	}
	if v := os.Getenv("S3_REGION"); v != "" {
		cfg.Storage.S3.Region = v
	}
	if v := os.Getenv("S3_BUCKET"); v != "" {
		cfg.Storage.S3.Bucket = v
	}
	// secretEnv, not os.Getenv: these are credentials and arrive as mounted
	// files in a deployment.
	if v := secretEnv("S3_ACCESS_KEY"); v != "" {
		cfg.Storage.S3.AccessKey = v
	}
	if v := secretEnv("S3_SECRET_KEY"); v != "" {
		cfg.Storage.S3.SecretKey = v
	}
	if v := os.Getenv("S3_USE_PATH_STYLE"); v != "" {
		cfg.Storage.S3.UsePathStyle = v == "true"
	}
	if v := os.Getenv("S3_PART_SIZE_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			cfg.Storage.S3.PartSizeBytes = n
		}
	}
	if v := os.Getenv("S3_UPLOAD_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Storage.S3.UploadConcurrency = n
		}
	}

	// PROVISIONING CONFIG
	if sdPayloadDir := os.Getenv("PROVISIONING_SD_PAYLOAD_DIR"); sdPayloadDir != "" {
		cfg.Provisioning.SDPayloadDir = sdPayloadDir
	}
	if httpBootDir := os.Getenv("PROVISIONING_HTTP_BOOT_DIR"); httpBootDir != "" {
		cfg.Provisioning.HTTPBootDir = httpBootDir
	}
	if sourceOfferDir := os.Getenv("SOURCE_OFFER_DIR"); sourceOfferDir != "" {
		cfg.Provisioning.SourceOfferDir = sourceOfferDir
	}
	if v := os.Getenv("PROVISIONING_HTTP_BOOT_FAIL_OPEN"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Provisioning.HTTPBootFailOpen = b
		}
	}
	if v := os.Getenv("PROVISIONING_BOOT_FAILSAFE_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Provisioning.BootFailsafeThreshold = n
		}
	}
	if v := os.Getenv("PROVISIONING_BOOT_HOST"); v != "" {
		cfg.Provisioning.BootHost = v
	}
	if signerURL := os.Getenv("EEPROM_SIGNER_URL"); signerURL != "" {
		cfg.EEPROMSigner.URL = signerURL
	}
	if signerToken := secretEnv("EEPROM_SIGNER_TOKEN"); signerToken != "" {
		cfg.EEPROMSigner.Token = signerToken
	}

	// SECRETS CONFIG
	if encryptionKey := secretEnv("SECRETS_ENCRYPTION_KEY"); encryptionKey != "" {
		cfg.Secrets.EncryptionKey = encryptionKey
	}
	if old := secretEnv("SECRETS_ENCRYPTION_KEY_OLD"); old != "" {
		for _, k := range strings.Split(old, ",") {
			if k = strings.TrimSpace(k); k != "" {
				cfg.Secrets.OldEncryptionKeys = append(cfg.Secrets.OldEncryptionKeys, k)
			}
		}
	}
	if cdnKey := secretEnv("CDN_SIGNING_KEY"); cdnKey != "" {
		cfg.Secrets.CDNSigningKey = cdnKey
	}
	if v := os.Getenv("SECRETS_ENCRYPTION_REWRAP"); v == "true" || v == "1" {
		cfg.Secrets.Rewrap = true
	}

	// IMAGE CONFIG (quotas; 0/false = unrestricted)
	if v := os.Getenv("WEBAUTHN_RP_ID"); v != "" {
		cfg.Server.WebAuthnRPID = v
	}
	if v := os.Getenv("IMAGE_MAX_DECOMPRESSED_SIZE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			cfg.Image.MaxDecompressedSize = n
		}
	}
	if v := os.Getenv("IMAGE_ALLOW_PLAINTEXT_URL"); v != "" {
		cfg.Image.AllowPlaintextURL = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("IMAGE_MAX_UPLOAD_SIZE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			cfg.Image.MaxUploadSize = n
		}
	}
	if v := os.Getenv("IMAGE_MAX_PER_ACCOUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Image.MaxPerAccount = n
		}
	}
	if v := os.Getenv("IMAGE_CAPTURE_INFLIGHT_WINDOW_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Image.CaptureInFlightWindowMinutes = n
		}
	}
	if v := os.Getenv("IMAGE_MAX_CONCURRENT_UPLOADS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Image.MaxConcurrentUploads = n
		}
	}
	if v := os.Getenv("IMAGE_MAX_CONCURRENT_UPLOADS_PER_ACCOUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Image.MaxConcurrentUploadsPerAccount = n
		}
	}
	if v := os.Getenv("IMAGE_XZ_MEMLIMIT_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Image.XZMemLimitMB = n
		}
	}
	if v := os.Getenv("IMAGE_COMPRESSION_MEMORY_BUDGET_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Image.CompressionMemoryBudgetMB = n
		}
	}
	if v := os.Getenv("IMAGE_MAX_CONCURRENT_COMPRESSIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Image.MaxConcurrentCompressions = n
		}
	}
	if v := os.Getenv("IMAGE_MAX_STAGING_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			cfg.Image.MaxStagingBytes = n
		}
	}
	if v := os.Getenv("IMAGE_COMPRESSION_TIMEOUT_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Image.CompressionTimeoutMinutes = n
		}
	}
	if v := os.Getenv("MEMBER_MAX_INVITES_PER_DAY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Member.MaxInvitesPerDay = n
		}
	}

	// PER-TENANT QUOTAS. All five are 0 = unlimited, and all
	// five default ARMED — see QuotaConfig for why these differ from the storage
	// quotas beside them.
	if v := os.Getenv("API_RATE_LIMIT_PER_ACCOUNT_RPS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			cfg.Quota.APIRequestsPerSecond = f
		}
	}
	if v := os.Getenv("API_RATE_LIMIT_PER_ACCOUNT_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Quota.APIBurst = n
		}
	}
	if v := os.Getenv("NODE_MAX_PER_ACCOUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Quota.MaxNodesPerAccount = n
		}
	}
	if v := os.Getenv("NODE_SERIAL_LEASE_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Quota.SerialLeaseDays = n
		}
	}
	if v := os.Getenv("MEMBER_MAX_PER_ACCOUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Quota.MaxMembersPerAccount = n
		}
	}
	if v := os.Getenv("SECRET_MAX_PER_ACCOUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Quota.MaxSecretsPerAccount = n
		}
	}
	if v := os.Getenv("UTILITY_MAX_ASSIGNMENTS_PER_ACCOUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Quota.MaxUtilityAssignmentsPerAccount = n
		}
	}
	if v := os.Getenv("UTILITY_MAX_CONCURRENT_APPLIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Quota.MaxConcurrentUtilityApplies = n
		}
	}
	if v := os.Getenv("AGENT_MAX_STREAMS_PER_NODE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Quota.MaxAgentStreamsPerNode = n
		}
	}
	if v := os.Getenv("AGENT_STREAM_RATE_LIMIT_RPS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			cfg.Quota.AgentStreamMessagesPerSecond = f
		}
	}
	if v := os.Getenv("AGENT_STREAM_RATE_LIMIT_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Quota.AgentStreamBurst = n
		}
	}
	// Positive only: 0 would fall through to the code default in the gate
	// anyway, and accepting it here would read as "unlimited" to whoever set
	// it. There is no unlimited setting for this bound.
	if v := os.Getenv("AGENT_MAX_PREAUTH_STREAMS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Quota.MaxPreAuthStreams = n
		}
	}
	// Same rule as the global gate: positive only, no unlimited setting.
	if v := os.Getenv("AGENT_MAX_PREAUTH_STREAMS_PER_SOURCE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Quota.MaxPreAuthStreamsPerSource = n
		}
	}
	if v := os.Getenv("IMAGE_MAX_BYTES_PER_ACCOUNT"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			cfg.Image.MaxBytesPerAccount = n
		}
	}
	if v := os.Getenv("IMAGE_MAX_BYTES_INSTANCE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			cfg.Image.MaxBytesInstance = n
		}
	}
	if v := os.Getenv("IMAGE_COMPRESS_REPLACE_ORIGINAL"); v != "" {
		cfg.Image.ReplaceOriginalOnCompress = v == "true" || v == "1"
	}

	// NODE FILE QUOTAS (0 = unlimited; unset keeps the shipped defaults)
	if v := os.Getenv("NODE_FILE_MAX_VERSIONS_PER_PATH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.NodeFile.MaxVersionsPerPath = n
		}
	}
	if v := os.Getenv("NODE_FILE_MAX_PER_NODE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.NodeFile.MaxFilesPerNode = n
		}
	}
	if v := os.Getenv("NODE_FILE_MAX_BYTES_PER_NODE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			cfg.NodeFile.MaxBytesPerNode = n
		}
	}
	if v := os.Getenv("NODE_FILE_MAX_BYTES_PER_ACCOUNT"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			cfg.NodeFile.MaxBytesPerAccount = n
		}
	}

	// RETENTION (negative values are ignored; 0 explicitly means unlimited)
	if v := os.Getenv("NODE_COMMAND_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Retention.NodeCommandDays = n
		}
	}
	if v := os.Getenv("AUDIT_EVENT_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Retention.AuditEventDays = n
		}
	}

	// WORKFLOW CONFIG (negative values are ignored; 0 explicitly disables)
	if v := os.Getenv("WORKFLOW_EXECUTION_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Workflow.ExecutionRetentionDays = n
		}
	}
	if v := os.Getenv("WORKFLOW_MAX_ACTIVE_EXECUTIONS_PER_ACCOUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Workflow.MaxActiveExecutionsPerAccount = n
		}
	}
	if v := os.Getenv("WORKFLOW_MAX_CRON_PLAYBOOKS_PER_ACCOUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Workflow.MaxCronPlaybooksPerAccount = n
		}
	}
	if v := os.Getenv("WORKFLOW_MAX_RUNNING_NODES_PER_ACCOUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Workflow.MaxRunningNodesPerAccount = n
		}
	}

	// Derived, after every override has been applied: a production environment
	// must not log every SQL statement with its parameter values.
	cfg.Database.ProductionLogging = cfg.IsProduction()

	return cfg, nil
}
