package sql

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"gorm.io/gorm"
)

// Verify interface implementation
var _ repository.NodeRepository = (*NodeRepository)(nil)

// NodeRepository is the implementation of the node repository using SQL
type NodeRepository struct {
	db *gorm.DB
	// enc protects nodes.default_password at rest. Required: the provisioning
	// password is a device login, so a repository built without it would write
	// the one credential this table holds in the clear.
	enc Encryptor
	// countAdmittedIn reads the admitted count inside the admission
	// transaction. A field only so the DSN test can make the count fail the
	// way a real statement fails inside a Postgres transaction; nil means
	// admittedScope's count.
	countAdmittedIn func(tx *gorm.DB) (int64, error)
}

// autoMigrate creates the necessary database tables
func (r *NodeRepository) autoMigrate() error {
	return r.db.AutoMigrate(&NodeModel{})
}

// Setup initializes the repository and creates tables if needed
func (r *NodeRepository) Setup() error {
	if err := r.autoMigrate(); err != nil {
		return err
	}
	// Expression index for serial lookups: the uniqueness guard runs
	// lower(fingerprint->>'hardware_serial') = ? on every master-token
	// ConnectionInit (i.e. every fleet reconnect), which is a full-table scan
	// without it. Non-unique on purpose: short WebUSB placeholders and
	// degenerate serials may legitimately repeat; uniqueness is enforced by
	// the app-level guard until a dedicated de-duped column lands.
	if err := r.db.Exec(
		"CREATE INDEX IF NOT EXISTS idx_nodes_hardware_serial_lower ON nodes ((lower(fingerprint->>'hardware_serial')))",
	).Error; err != nil {
		return err
	}
	return r.backfillDeviceProfileAppliedAt()
}

// backfillDeviceProfileAppliedAt dates the applies that were recorded before
// the column existed, from the command history that already holds the answer.
//
// Without it every node in the fleet reads as "cannot tell" until its NEXT
// apply, and the reboot button this column exists to retire stays offered on
// all of them — the fix would ship and change nothing visible. Rows whose apply
// command has aged out of `node_commands` retention keep a NULL, which is the
// honest answer and fails open exactly like an unknown.
//
// Idempotent and self-limiting: it only touches rows that HAVE an applied hash
// and NO timestamp, so a later run finds nothing to do. Never invents a time —
// a node with no surviving successful apply command is left alone.
//
// On an empty database the command table does not exist yet — its own
// repository creates it after this one — and there is nothing to date, so
// the backfill is skipped rather than failed: a statement naming a missing
// relation would refuse the first boot of every new install.
func (r *NodeRepository) backfillDeviceProfileAppliedAt() error {
	if !r.db.Migrator().HasTable("node_commands") {
		return nil
	}
	return r.db.Exec(`
		UPDATE nodes n
		SET device_profile_applied_at = c.applied_at
		FROM (
			SELECT node_id, MAX(updated_at) AS applied_at
			FROM node_commands
			WHERE slug = 'apply_device_profile' AND status = 'success'
			GROUP BY node_id
		) c
		WHERE c.node_id = n.id
		  AND n.device_profile_applied_hash <> ''
		  AND n.device_profile_applied_at IS NULL
	`).Error
}

// NewNodeRepository creates a new node repository
func NewNodeRepository(db *gorm.DB, enc Encryptor) *NodeRepository {
	if enc == nil {
		panic("NodeRepository requires an encryptor")
	}
	repo := &NodeRepository{
		db:  db,
		enc: enc,
	}
	return repo
}

// toDomain converts a row and decrypts its provisioning password. Every read
// path goes through it — a read that used NodeModel.ToDomain directly would
// hand the caller the at-rest envelope.
func (r *NodeRepository) toDomain(m *NodeModel) *node.Node {
	n := m.ToDomain()
	n.DefaultPassword, n.DefaultPasswordUnreadable = decryptProvisioningPassword(
		r.enc, m.DefaultPassword, m.DefaultPasswordEnc, "nodes."+m.ID.String())
	n.UniqueToken, n.UniqueTokenUnreadable = decryptDeviceToken(
		r.enc, m.UniqueToken, m.UniqueTokenEnc, "nodes."+m.ID.String())
	return n
}

// fromDomain fills a row from a node and encrypts its provisioning password
// and unique token. Returns the columns to omit from the write: when a loaded
// value was undecryptable and the caller supplied no replacement, its columns
// are left untouched rather than overwritten with the blank the read produced —
// a rotation that restores the right key can still recover the row.
func (r *NodeRepository) fromDomain(m *NodeModel, n *node.Node) ([]string, error) {
	m.FromDomain(n)
	var omit []string
	if n.DefaultPasswordUnreadable && n.DefaultPassword == "" {
		omit = append(omit, "default_password", "default_password_enc")
	} else {
		stored, marker, err := encryptProvisioningPassword(r.enc, n.DefaultPassword)
		if err != nil {
			return nil, err
		}
		m.DefaultPassword = stored
		m.DefaultPasswordEnc = marker
	}
	// Same preservation rule, higher stakes: overwriting an unreadable token
	// with an encrypted blank would permanently unauthenticate the device,
	// while leaving the ciphertext lets a key rotation recover it.
	if n.UniqueTokenUnreadable && n.UniqueToken == "" {
		omit = append(omit, "unique_token", "unique_token_enc")
	} else {
		stored, marker, err := encryptDeviceToken(r.enc, n.UniqueToken)
		if err != nil {
			return nil, err
		}
		m.UniqueToken = stored
		m.UniqueTokenEnc = marker
	}
	return omit, nil
}

// UpdateOperatorEditable writes ONLY the columns a human edits on a node —
// name, notes, the provisioning defaults — and touches nothing else.
//
// The alternative, and what the operator update used to do, is a full-row Save
// of a row read moments earlier. A node row is written continuously by the gRPC
// layer (status, connection state, hardware facts, boot device, agent version,
// applied-generation hashes), so an operator saving a Notes edit would revert
// whatever landed in between — including the `restarting` pin, which exists
// precisely to survive a reboot, after which handleDisconnection marks the node
// offline instead of restarting. The window is small and the failure is silent,
// which is the combination that makes it survive testing.
//
// This is a narrower fix than an `updated_at` precondition on purpose: a
// conflict error would need a client that knows how to retry, while an operator
// editing Notes has no opinion about the agent version and simply should not be
// writing it. Two operators editing the SAME field still last-write-wins; that
// is a different, and far more visible, kind of conflict.
func (r *NodeRepository) UpdateOperatorEditable(ctx context.Context, n *node.Node) error {
	model := &NodeModel{}
	omitPassword, err := r.fromDomain(model, n)
	if err != nil {
		return err
	}

	updates := map[string]interface{}{
		"name":             model.Name,
		"notes":            model.Notes,
		"ssh_key_id":       model.SSHKeyID,
		"default_username": model.DefaultUsername,
		"updated_at":       time.Now(),
	}
	// The token is not operator-editable; this writer never touches it.
	// An unreadable stored password is left exactly as it is: re-encrypting a
	// value we could not decrypt would replace the operator's credential with
	// an empty one and report success.
	if len(omitPassword) == 0 {
		updates["default_password"] = model.DefaultPassword
		updates["default_password_enc"] = model.DefaultPasswordEnc
	}

	result := r.db.WithContext(ctx).
		Model(&NodeModel{}).
		Where("id = ? AND account_id = ?", n.ID, n.AccountID).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrNodeNotFound
	}
	return nil
}

// UpdateNodeColumns writes an explicit set of columns on one node and nothing
// else, stamping updated_at.
//
// It is the general form of the rule UpdateOperatorEditable follows: an
// operator-driven write must name what it changes. A node row is written
// continuously by the gRPC layer, so anything that reads the row, edits a field
// and saves it back reverts whatever arrived in between.
//
// Callers pass column names, not struct fields, because that is what makes the
// blast radius reviewable at the call site.
func (r *NodeRepository) UpdateNodeColumns(ctx context.Context, accountID, nodeID uuid.UUID, columns map[string]interface{}) error {
	if len(columns) == 0 {
		return nil
	}
	// Keys are resolved through the model's schema, the way GORM's
	// Updates(map) resolves them: it accepts the struct field name
	// ("Status") as well as the column ("status"), so a check on the column
	// spelling alone let the field spelling write the admission columns.
	stmt := &gorm.Statement{DB: r.db}
	if err := stmt.Parse(&NodeModel{}); err != nil {
		return fmt.Errorf("UpdateNodeColumns: resolving the node schema: %w", err)
	}
	for key := range columns {
		column := key
		if field := stmt.Schema.LookUpField(key); field != nil {
			column = field.DBName
		}
		if column == "status" || column == "approval_status" {
			return fmt.Errorf("UpdateNodeColumns does not write %s: it decides device-cap admission and has its own writer", column)
		}
	}
	updates := make(map[string]interface{}, len(columns)+1)
	for k, v := range columns {
		updates[k] = v
	}
	updates["updated_at"] = time.Now()

	result := r.db.WithContext(ctx).
		Model(&NodeModel{}).
		Where("id = ? AND account_id = ?", nodeID, accountID).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrNodeNotFound
	}
	return nil
}

// systemReportColumns are the columns a device's system-info report owns.
// Nothing an operator writes is here, and neither is anything that decides
// admission, so a report can never carry a stale operator value or a stale
// status back to the row.
var systemReportColumns = map[string]bool{
	"system_info":          true,
	"storage_devices":      true,
	"last_boot_at":         true,
	"bootloader_version":   true,
	"bootloader_timestamp": true,
	"hostname":             true,
	"ipv4":                 true,
}

// UpdateSystemReport writes the named report-owned columns of n, and
// optionally completes a pending reboot-to-recovery, without a full-row save.
// See the port for the contract.
func (r *NodeRepository) UpdateSystemReport(ctx context.Context, n *node.Node, columns []string, completeRecoveryRevert bool) (bool, error) {
	for _, c := range columns {
		if !systemReportColumns[c] {
			return false, fmt.Errorf("UpdateSystemReport does not write %s: it is not a column a system-info report owns", c)
		}
	}
	// Only the plain columns are read from the model; the credential columns
	// it also fills are never written here, so nothing is encrypted.
	model := &NodeModel{}
	model.FromDomain(n)
	values := map[string]interface{}{
		"system_info":          model.SystemInfo,
		"storage_devices":      model.StorageDevices,
		"last_boot_at":         model.LastBootAt,
		"bootloader_version":   model.BootloaderVersion,
		"bootloader_timestamp": model.BootloaderTimestamp,
		"hostname":             model.Hostname,
		"ipv4":                 model.IPv4,
	}

	reverted := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := make(map[string]interface{}, len(columns)+1)
		for _, c := range columns {
			updates[c] = values[c]
		}
		updates["updated_at"] = time.Now()
		result := tx.Model(&NodeModel{}).
			Where("id = ? AND account_id = ?", n.ID, n.AccountID).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return app_errors.ErrNodeNotFound
		}
		if !completeRecoveryRevert {
			return nil
		}
		// Conditional on the revert the report saw: a revert already
		// completed, or a newer reboot-to-recovery armed in between, is
		// left exactly as it is.
		result = tx.Model(&NodeModel{}).
			Where("id = ? AND account_id = ? AND recovery_revert_pending = ? AND recovery_prev_boot_to_os = ?",
				n.ID, n.AccountID, true, n.RecoveryPrevBootToOS).
			Updates(map[string]interface{}{
				"boot_to_os":               n.BootToOS,
				"recovery_revert_pending":  false,
				"recovery_prev_boot_to_os": "",
			})
		if result.Error != nil {
			return result.Error
		}
		reverted = result.RowsAffected > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return reverted, nil
}

// BackfillDefaultPasswordEncryption encrypts the provisioning passwords stored
// before the marker column existed. Non-fatal by contract — see
// backfillProvisioningPasswords.
func (r *NodeRepository) BackfillDefaultPasswordEncryption() (int, error) {
	return backfillProvisioningPasswords(r.db, r.enc, "nodes", "id")
}

// Create creates a new node
func (r *NodeRepository) Create(ctx context.Context, node *node.Node) error {
	model := &NodeModel{}
	if _, err := r.fromDomain(model, node); err != nil {
		return err
	}

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return result.Error
	}

	return nil
}

// Update updates an existing node
func (r *NodeRepository) Update(ctx context.Context, node *node.Node) error {
	return r.updateIn(r.db.WithContext(ctx), node, false)
}

// updateIn is the full-row write. admission is true only for
// UpdateUnderDeviceCap, inside the device-cap lock; every other caller gets
// the guard below, so a plain save can never be the row's admission.
func (r *NodeRepository) updateIn(db *gorm.DB, node *node.Node, admission bool) error {
	model := &NodeModel{}
	omitPassword, err := r.fromDomain(model, node)
	if err != nil {
		return err
	}

	// Update the updatedAt timestamp
	model.UpdatedAt = time.Now()

	// The boot-loop failsafe RUNTIME columns are owned exclusively by the targeted
	// atomic methods (IncrementBootAttempt/SetBootFailsafe/ResetBootFailsafe).
	// Omit them from this full-row write so a concurrent full-row save can
	// never clobber a concurrent increment. The operator-set
	// override (boot_failsafe_threshold) is NOT omitted — it rides the normal update.
	// bootloader_flashed_at is owned by the settle hook's UpdateNodeColumns for
	// the same reason: a read-modify-write that runs around the moment a flash
	// settles (a connect) would, from a read that predates the stamp, NULL it
	// back out. System-info reports write through UpdateSystemReport instead.
	omit := append([]string{"boot_attempt_count", "last_boot_attempt_at", "boot_failsafe_at", "bootloader_flashed_at"}, omitPassword...)
	q := db.Where("id = ? AND account_id = ?", node.ID, node.AccountID)
	guarded := !admission && node.IsAdmitted()
	if guarded {
		// An admitted new state lands only on a row that is admitted now.
		// The save is a read-modify-write, and a copy read before the row
		// stepped out of the admitted set would otherwise put it back
		// without the device-cap lock.
		q = admittedScope(q)
	}
	result := updateAllColumns(q.Omit(omit...), model)
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		if guarded {
			return r.admissionRefusal(db, node.ID, node.AccountID, node.Status)
		}
		return app_errors.ErrNodeNotFound
	}

	return nil
}

// admissionRefusal names why a guarded write matched no row: the row is
// gone (ErrNodeNotFound), or it exists and the write would have admitted it
// (*node.AdmissionRequiredError). accountID may be uuid.Nil for the
// status-only writes, which are keyed on the node id alone.
func (r *NodeRepository) admissionRefusal(db *gorm.DB, nodeID, accountID uuid.UUID, requested node.NodeStatus) error {
	q := db.Session(&gorm.Session{NewDB: true}).Model(&NodeModel{}).Where("id = ?", nodeID)
	if accountID != uuid.Nil {
		q = q.Where("account_id = ?", accountID)
	}
	var stored struct {
		AccountID uuid.UUID
		Status    string
	}
	if err := q.Select("account_id", "status").Take(&stored).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return app_errors.ErrNodeNotFound
		}
		return err
	}
	return &node.AdmissionRequiredError{NodeID: nodeID, AccountID: stored.AccountID,
		Stored: node.NodeStatus(stored.Status), Requested: requested}
}

// GetByID retrieves a node by ID within an account
func (r *NodeRepository) GetByID(ctx context.Context, accountID, nodeID uuid.UUID) (*node.Node, error) {
	var model NodeModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", nodeID, accountID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNodeNotFound
		}
		return nil, result.Error
	}

	return r.toDomain(&model), nil
}

// GetAllByAccountID retrieves all nodes for a specific account
// CountByAccountID counts the account's live nodes for the per-account fleet
// cap. Deleted nodes live in node_tombstones and are not counted here.
func (r *NodeRepository) CountByAccountID(ctx context.Context, accountID uuid.UUID) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&NodeModel{}).
		Where("account_id = ?", accountID).
		Count(&n).Error
	return n, err
}

// DeviceCapLockClass is the advisory-lock class the device entitlement's
// admission takes. ONE key for the whole install, deliberately: the count it
// guards is instance-wide, so a per-account key would let two accounts admit
// past the cap together. The per-account limit's count is taken under the
// same key, so one admission holds one lock whichever limits apply.
const DeviceCapLockClass = 0x4F524443 // "ORDC"

// admittedScope is the SQL form of node.IsAdmitted. The status list comes
// from the domain so the two cannot drift.
func admittedScope(q *gorm.DB) *gorm.DB {
	return q.Where("approval_status = ?", string(node.ApprovalStatusApproved)).
		Where("status NOT IN ?", preConnectStatuses())
}

func preConnectStatuses() []string {
	statuses := make([]string, 0, len(node.NotAdmittedStatuses))
	for _, s := range node.NotAdmittedStatuses {
		statuses = append(statuses, string(s))
	}
	return statuses
}

func (r *NodeRepository) CountAdmittedNodes(ctx context.Context) (int64, error) {
	var n int64
	err := admittedScope(r.db.WithContext(ctx).Model(&NodeModel{})).Count(&n).Error
	return n, err
}

// CountAdmittedNodesForAccount counts one account's admitted devices with
// the same predicate (admittedScope) the instance-wide count uses.
func (r *NodeRepository) CountAdmittedNodesForAccount(ctx context.Context, accountID uuid.UUID) (int64, error) {
	var n int64
	err := admittedScope(r.db.WithContext(ctx).Model(&NodeModel{}).Where("account_id = ?", accountID)).Count(&n).Error
	return n, err
}

// underDeviceCap runs write only while every limit in limits has room, not
// counting the row being admitted (exclude): the instance-wide admitted count
// below the instance cap, and the admitting account's own admitted count
// below its limit. The advisory lock, the counts and the write run in ONE
// transaction, so admissions arriving together serialize instead of all
// reading the same count and all passing. The lock is the same single key
// for both limits: the account count is taken under it too, so two
// admissions into one account cannot both pass its last slot, and an install
// whose instance cap is unlimited still locks while an account limit applies.
//
// A lock or a count that FAILS refuses with *entitlement.DeviceCapUnverifiedError
// and writes nothing: the closure's error rolls the transaction back, and
// there is no second attempt outside it. (In Postgres a failed statement
// aborts its transaction anyway — every later statement answers SQLSTATE
// 25P02 — so nothing after the failure could have landed inside it.) A
// write outside the lock would let anyone who can make the count fail — a
// statement or lock timeout on the connection is enough — admit past the cap.
func (r *NodeRepository) underDeviceCap(ctx context.Context, limits repository.AdmissionLimits, exclude uuid.UUID, write func(db *gorm.DB) error) (repository.DeviceCapAdmission, error) {
	db := r.db.WithContext(ctx)
	if !limits.Bounded() {
		return repository.DeviceCapAdmission{}, write(db)
	}
	cap := limits.InstanceCap
	var admission repository.DeviceCapAdmission
	began := false
	err := db.Transaction(func(tx *gorm.DB) error {
		began = true
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", DeviceCapLockClass).Error; err != nil {
			return &entitlement.DeviceCapUnverifiedError{Cap: cap, Cause: fmt.Errorf("taking the device-cap lock: %w", err)}
		}
		if cap > 0 {
			count, err := r.countAdmitted(tx, exclude)
			if err != nil {
				return &entitlement.DeviceCapUnverifiedError{Cap: cap, Cause: fmt.Errorf("counting admitted nodes: %w", err)}
			}
			admission.Count = count
			if count >= int64(cap) {
				return &entitlement.DeviceCapError{Cap: cap, Count: count}
			}
		}
		if limits.AccountID != uuid.Nil && limits.Account.Applies() {
			own, err := r.countAdmittedForAccount(tx, limits.AccountID, exclude)
			if err != nil {
				return &entitlement.DeviceCapUnverifiedError{Cap: cap, Cause: fmt.Errorf("counting the account's admitted nodes: %w", err)}
			}
			admission.AccountCount = own
			if own >= int64(limits.Account.Limit) {
				return &entitlement.AccountDeviceLimitError{AccountID: limits.AccountID, Limit: limits.Account.Limit,
					Count: own, Plan: limits.Account.Plan, Until: limits.Account.Until, Contact: limits.Account.Contact}
			}
		}
		return write(tx)
	})
	if err != nil && !began {
		// The transaction never started, so neither did the check.
		return admission, &entitlement.DeviceCapUnverifiedError{Cap: cap, Cause: fmt.Errorf("beginning the admission: %w", err)}
	}
	return admission, err
}

func (r *NodeRepository) countAdmitted(tx *gorm.DB, exclude uuid.UUID) (int64, error) {
	if r.countAdmittedIn != nil {
		return r.countAdmittedIn(tx)
	}
	q := admittedScope(tx.Model(&NodeModel{}))
	if exclude != uuid.Nil {
		q = q.Where("id <> ?", exclude)
	}
	var count int64
	err := q.Count(&count).Error
	return count, err
}

// countAdmittedForAccount is countAdmitted restricted to one account, inside
// the admission transaction.
func (r *NodeRepository) countAdmittedForAccount(tx *gorm.DB, accountID, exclude uuid.UUID) (int64, error) {
	q := admittedScope(tx.Model(&NodeModel{}).Where("account_id = ?", accountID))
	if exclude != uuid.Nil {
		q = q.Where("id <> ?", exclude)
	}
	var count int64
	err := q.Count(&count).Error
	return count, err
}

// UpdateUnderDeviceCap is the full-row Update of a node whose new state is
// admitted, made only while every OTHER row leaves room under limits
// (underDeviceCap). Refused, nothing is written.
func (r *NodeRepository) UpdateUnderDeviceCap(ctx context.Context, n *node.Node, limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
	return r.underDeviceCap(ctx, limits, n.ID, func(db *gorm.DB) error {
		return r.updateIn(db, n, true)
	})
}

// UpdateStatusUnderDeviceCap is the status-only admission: the write the
// guarded UpdateStatus refuses, made only while every OTHER row leaves room
// under limits (underDeviceCap). Refused, nothing is written.
func (r *NodeRepository) UpdateStatusUnderDeviceCap(ctx context.Context, nodeID uuid.UUID, status, slug string, limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
	return r.underDeviceCap(ctx, limits, nodeID, func(db *gorm.DB) error {
		return r.writeStatus(db, nodeID, status, slug, true)
	})
}

// ApproveUnderDeviceCap sets the node's approval status to approved only
// while every OTHER row leaves room under limits. Refused, nothing is
// written.
func (r *NodeRepository) ApproveUnderDeviceCap(ctx context.Context, nodeID uuid.UUID, limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
	return r.underDeviceCap(ctx, limits, nodeID, func(db *gorm.DB) error {
		return r.updateApprovalStatusIn(db, nodeID, string(node.ApprovalStatusApproved))
	})
}

// CreateUnderDeviceCap inserts the node only while limits leave room
// (underDeviceCap). See the port for the refusal on a failed lock or count.
func (r *NodeRepository) CreateUnderDeviceCap(ctx context.Context, n *node.Node, limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
	model := &NodeModel{}
	if _, err := r.fromDomain(model, n); err != nil {
		return repository.DeviceCapAdmission{}, err
	}
	return r.underDeviceCap(ctx, limits, uuid.Nil, func(db *gorm.DB) error {
		return db.Create(model).Error
	})
}

func (r *NodeRepository) GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]node.Node, error) {
	var models []NodeModel
	result := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at DESC").
		Limit(listRunawayCeiling).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	// Convert models to domain objects
	nodes := make([]node.Node, len(models))
	for i, model := range models {
		domainNode := r.toDomain(&model)
		nodes[i] = *domainNode
	}

	return nodes, nil
}

// Delete deletes a node by ID within an account
func (r *NodeRepository) Delete(ctx context.Context, accountID, nodeID uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", nodeID, accountID).
		Delete(&NodeModel{})
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrNodeNotFound
	}

	return nil
}

// GetByIDAndAccount retrieves a node by ID and account ID (for registration validation)
func (r *NodeRepository) GetByIDAndAccount(ctx context.Context, nodeID, accountID uuid.UUID) (*node.Node, error) {
	var model NodeModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", nodeID, accountID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNodeNotFound
		}
		return nil, result.Error
	}

	return r.toDomain(&model), nil
}

// UpdateStatus updates only the status field, and CLEARS the pin cause.
//
// Clearing is the default on purpose: pinned_by_slug describes why the node is
// in a transient status, so it must not outlive the status that carried it. A
// writer that means to record a cause says so with UpdateStatusPinnedBy; every
// other status write in the product — a reconnect, an offline sweep, a WebUSB
// flash — passes through here and leaves nothing stale behind. Kept as one
// statement so the two columns cannot disagree.
func (r *NodeRepository) UpdateStatus(ctx context.Context, nodeID uuid.UUID, status string) error {
	return r.updateStatus(ctx, nodeID, status, "")
}

// UpdateStatusPinnedBy updates the status and records the command slug that
// imposed it. See node.Node.PinnedBySlug for why the cause is a column.
func (r *NodeRepository) UpdateStatusPinnedBy(ctx context.Context, nodeID uuid.UUID, status, slug string) error {
	return r.updateStatus(ctx, nodeID, status, slug)
}

func (r *NodeRepository) updateStatus(ctx context.Context, nodeID uuid.UUID, status, slug string) error {
	return r.writeStatus(r.db.WithContext(ctx), nodeID, status, slug, false)
}

// writeStatus is the one status-only statement. Unless it is the admission
// itself (inside the device-cap lock), a move to a status outside the
// pre-connect set must not match an APPROVED row still in that set: that
// move is the row's admission, and the device cap is enforced only there.
// Every other status move — within the admitted set, or into a pre-connect
// status — is unconditional.
func (r *NodeRepository) writeStatus(db *gorm.DB, nodeID uuid.UUID, status, slug string, admission bool) error {
	q := db.Model(&NodeModel{}).Where("id = ?", nodeID)
	guarded := !admission && !node.IsPreConnectStatus(node.NodeStatus(status))
	if guarded {
		q = q.Where("NOT (approval_status = ? AND status IN ?)",
			string(node.ApprovalStatusApproved), preConnectStatuses())
	}
	result := q.Updates(map[string]interface{}{
		"status":         status,
		"pinned_by_slug": slug,
		"updated_at":     time.Now(),
	})
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		if guarded {
			return r.admissionRefusal(db, nodeID, uuid.Nil, node.NodeStatus(status))
		}
		return app_errors.ErrNodeNotFound
	}

	return nil
}

// CommitWebUSBFlash is conditional on the stored status in the statement
// itself; see the port for why a read-then-write loses to a connect.
func (r *NodeRepository) CommitWebUSBFlash(ctx context.Context, accountID, nodeID uuid.UUID, flashedAt time.Time) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&NodeModel{}).
		Where("id = ? AND account_id = ? AND status = ?", nodeID, accountID, string(node.StatusFlashing)).
		Updates(map[string]interface{}{
			"status":                string(node.StatusReadyForAdoption),
			"pinned_by_slug":        "",
			"bootloader_flashed_at": flashedAt,
			"updated_at":            time.Now(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// MarkWebUSBReflashing is conditional in the statement itself; see the port
// for why a read-then-write loses to a first connect. Connect clears
// awaiting_first_connect in the same row write that sets the node online,
// so the first predicate alone closes the race; the status predicate keeps
// a connected row out of it even if the two ever came apart.
func (r *NodeRepository) MarkWebUSBReflashing(ctx context.Context, accountID, nodeID uuid.UUID) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&NodeModel{}).
		Where("id = ? AND account_id = ? AND awaiting_first_connect = ? AND status NOT IN ?",
			nodeID, accountID, true, connectedStatuses()).
		Updates(map[string]interface{}{
			"status":         string(node.StatusFlashing),
			"pinned_by_slug": "",
			"updated_at":     time.Now(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// connectedStatuses is the SQL form of node.IsInConnectedState.
func connectedStatuses() []string {
	return []string{string(node.StatusOnline), string(node.StatusMaintenance), string(node.StatusRestarting)}
}

// UpdateHeartbeat updates the last heartbeat timestamp
func (r *NodeRepository) UpdateHeartbeat(ctx context.Context, nodeID uuid.UUID, timestamp time.Time) error {
	result := r.db.WithContext(ctx).
		Model(&NodeModel{}).
		Where("id = ?", nodeID).
		Updates(map[string]interface{}{
			"last_heartbeat": timestamp,
			"last_seen_at":   timestamp,
			"updated_at":     time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrNodeNotFound
	}

	return nil
}

// MarkAwaitingReturn records what the node was doing when the platform last
// expected it back; ClearAwaitingReturn is its reconnect-side counterpart.
func (r *NodeRepository) MarkAwaitingReturn(ctx context.Context, nodeID uuid.UUID, slug string, since time.Time) error {
	return r.db.WithContext(ctx).Model(&NodeModel{}).
		Where("id = ?", nodeID).
		Updates(map[string]interface{}{
			"awaiting_return_after": slug,
			"awaiting_return_since": since,
		}).Error
}

func (r *NodeRepository) ClearAwaitingReturn(ctx context.Context, nodeID uuid.UUID) error {
	return r.db.WithContext(ctx).Model(&NodeModel{}).
		Where("id = ? AND awaiting_return_after IS NOT NULL", nodeID).
		Updates(map[string]interface{}{
			"awaiting_return_after": nil,
			"awaiting_return_since": nil,
		}).Error
}

// RotateAgentInstance stores the instance id the connecting agent process
// reports and returns the PREVIOUS one, so the caller can tell a surviving
// process from a restarted one. An empty new id writes nothing: "this agent
// does not report instance ids" must not erase what the last reporting agent
// said — the stored value still distinguishes that agent's own later reconnect.
func (r *NodeRepository) RotateAgentInstance(ctx context.Context, nodeID uuid.UUID, instanceID string) (string, error) {
	var prev string
	if err := r.db.WithContext(ctx).Model(&NodeModel{}).
		Where("id = ?", nodeID).
		Pluck("coalesce(agent_instance_id, '')", &prev).Error; err != nil {
		return "", err
	}
	if instanceID == "" || instanceID == prev {
		return prev, nil
	}
	if err := r.db.WithContext(ctx).Model(&NodeModel{}).
		Where("id = ?", nodeID).
		Update("agent_instance_id", instanceID).Error; err != nil {
		return "", err
	}
	return prev, nil
}

// UpdateAgentInfo updates agent version, agent kind and last seen
func (r *NodeRepository) UpdateAgentInfo(ctx context.Context, nodeID uuid.UUID, agentVersion, agentKind string, ledSources []string, lastSeen time.Time) error {
	updates := map[string]interface{}{
		"last_seen_at": lastSeen,
		"updated_at":   time.Now(),
	}

	// Only update agent version/kind if provided
	if agentVersion != "" {
		updates["agent_version"] = agentVersion
	}
	if agentKind != "" {
		updates["agent_kind"] = agentKind
	}
	// The LED-source advertisement tracks the CONNECTING agent exactly: an
	// agent that advertises nothing (pre-v2, a downgrade, or a watcher) clears
	// the column, so the UI never offers a source the running agent cannot
	// render.
	//
	// Gated on agentVersion, which is the same discriminator the two writes
	// above use: only a ConnectionInit carries a version, while the heartbeat
	// and status paths call this with ("", "", nil) purely to stamp
	// last_seen_at. Without the gate the else-branch fires on every heartbeat
	// and NULLs the advertisement ~30 seconds after each connect, so a node
	// that has just advertised its sources reads back empty.
	if agentVersion != "" {
		if len(ledSources) > 0 {
			updates["led_sources"] = strings.Join(ledSources, ",")
		} else {
			updates["led_sources"] = nil
		}
	}

	result := r.db.WithContext(ctx).
		Model(&NodeModel{}).
		Where("id = ?", nodeID).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrNodeNotFound
	}

	return nil
}

// GetStaleOnlineNodes finds nodes marked as online but haven't been seen recently
func (r *NodeRepository) GetStaleOnlineNodes(ctx context.Context, threshold time.Time) ([]node.Node, error) {
	var models []NodeModel
	result := r.db.WithContext(ctx).
		Where("status = ? AND (last_seen_at IS NULL OR last_seen_at < ?)", string(node.StatusOnline), threshold).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	nodes := make([]node.Node, len(models))
	for i, model := range models {
		nodes[i] = *r.toDomain(&model)
	}

	return nodes, nil
}

// GetStaleTransientNodes finds nodes stuck in a status that a command put them
// in, long past the point where the device could still be coming back.
//
// `restarting` and `maintenance` are TRANSIENT: they are set at dispatch and are
// meant to be cleared by the device reporting in. Nothing else clears them, and
// GetStaleOnlineNodes cannot see them because it matches `online` only — so a
// node that never returns from a reboot reads `restarting` forever, never
// `offline`, which means no pill, no alert, and no way to send it even a
// status_check (the dispatch path refuses non-queueable commands for a node
// that is neither online nor in maintenance). The only escape was deleting the
// node.
//
// `shutdown` is deliberately NOT included: the operator asked for it, the device
// is not expected back, and "shutdown" is the more informative answer than
// "offline". `flashing` is left alone for the same reason.
func (r *NodeRepository) GetStaleTransientNodes(ctx context.Context, threshold time.Time) ([]node.Node, error) {
	var models []NodeModel
	result := r.db.WithContext(ctx).
		Where("status IN ? AND (last_seen_at IS NULL OR last_seen_at < ?)",
			[]string{string(node.StatusRestarting), string(node.StatusMaintenance)},
			threshold).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	nodes := make([]node.Node, len(models))
	for i, model := range models {
		nodes[i] = *r.toDomain(&model)
	}

	return nodes, nil
}

// MarkAllOnlineNodesOffline marks all nodes with status 'online' as 'offline'
// This is used on server startup to reset stale connection states
func (r *NodeRepository) MarkAllOnlineNodesOffline(ctx context.Context) (int64, error) {
	result := r.db.WithContext(ctx).
		Model(&NodeModel{}).
		Where("status = ?", string(node.StatusOnline)).
		Updates(map[string]interface{}{
			"status":     string(node.StatusOffline),
			"updated_at": time.Now(),
		})
	if result.Error != nil {
		return 0, result.Error
	}

	return result.RowsAffected, nil
}

// GetByHardwareSerial retrieves a node by hardware serial (from fingerprint JSONB) within an account
// Used for master token authentication
func (r *NodeRepository) GetByHardwareSerial(ctx context.Context, accountID uuid.UUID, hardwareSerial string) (*node.Node, error) {
	var model NodeModel
	result := r.db.WithContext(ctx).
		Where("account_id = ? AND fingerprint->>'hardware_serial' = ?", accountID, hardwareSerial).
		First(&model)
	// Fallback: a WebUSB flasher only sees the short (low-word) serial, while the
	// agent stores the full 64-bit serial. Match a stored full serial by its
	// low-word suffix when the query serial is short — and, like the global
	// sibling below, only at the low-word width or longer: a master-token
	// handshake reaches this lookup with a peer-supplied serial the gRPC layer
	// checks only for non-emptiness, so a one-character "serial" would land on
	// whichever admitted node's serial ends in it. The service
	// refuses that landing on exact-serial grounds regardless; the floor keeps
	// the lookup from resolving it in the first place.
	if errors.Is(result.Error, gorm.ErrRecordNotFound) && isSuffixMatchableSerial(hardwareSerial) {
		result = r.db.WithContext(ctx).
			Where("account_id = ? AND right(fingerprint->>'hardware_serial', ?) = ?", accountID, len(hardwareSerial), hardwareSerial).
			First(&model)
	}
	// Symmetric fallback: the caller has the FULL serial (agent/SSH read) but the
	// stored value is a short WebUSB stamp — match the stored value as a suffix
	// of the query. Bounded to >=8 chars so degenerate stamps can't over-match.
	if errors.Is(result.Error, gorm.ErrRecordNotFound) && !isShortSerial(hardwareSerial) {
		result = r.db.WithContext(ctx).
			Where(
				"account_id = ? AND char_length(coalesce(fingerprint->>'hardware_serial', '')) BETWEEN 8 AND 15 AND right(?, char_length(fingerprint->>'hardware_serial')) = (fingerprint->>'hardware_serial')",
				accountID, hardwareSerial,
			).
			First(&model)
	}
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNodeNotFound
		}
		return nil, result.Error
	}

	return r.toDomain(&model), nil
}

// GetByHardwareSerialGlobal retrieves a node by hardware serial across all accounts.
// The HTTP-boot endpoint has only the serial (no account context), so it resolves
// the owning node this way. Serials SHOULD be globally unique but there is no DB
// constraint enforcing it yet (pre-GA: add one after de-duping) — so this is
// deterministic (newest bound fingerprint wins) and logs a collision, rather than
// returning a nondeterministic "first match" that could resolve to the wrong tenant.
func (r *NodeRepository) GetByHardwareSerialGlobal(ctx context.Context, hardwareSerial string) (*node.Node, error) {
	var models []NodeModel
	query := r.db.WithContext(ctx).Model(&NodeModel{})
	// Same low-word suffix match as GetByHardwareSerial (WebUSB short serial) —
	// and, like its account-scoped sibling, bounded to >= 8 chars so degenerate
	// stamps cannot over-match.
	//
	// The bound is what keeps this from being a cross-account lookup driven by
	// an unauthenticated URL segment: a shorter suffix would match across
	// accounts and turn the lookup into an enumeration primitive. A successful
	// suffix probe would also never trip the endpoint's abuse guard, which
	// counts failed lookups only.
	//
	// The suffix candidates are collected ALONGSIDE the exact ones, never only
	// when the exact match is empty: a short serial is not unique, so an exact
	// short stamp in one account must not hide the full-serial node in another
	// account whose device is the one asking (node.ResolveBootCandidate).
	if isSuffixMatchableSerial(hardwareSerial) {
		query = query.Where("(fingerprint->>'hardware_serial' = ? OR right(fingerprint->>'hardware_serial', ?) = ?)",
			hardwareSerial, len(hardwareSerial), hardwareSerial)
	} else {
		query = query.Where("fingerprint->>'hardware_serial' = ?", hardwareSerial)
	}
	if result := query.Order("created_at DESC").Find(&models); result.Error != nil {
		return nil, result.Error
	}
	if len(models) == 0 {
		return nil, app_errors.ErrNodeNotFound
	}
	candidates := make([]*node.Node, 0, len(models))
	for i := range models {
		candidates = append(candidates, r.toDomain(&models[i]))
	}
	resolved, ambiguous := node.ResolveBootCandidate(hardwareSerial, candidates)
	if ambiguous {
		// Resolving "the newest" is only defensible while every candidate
		// belongs to one account. Across accounts it hands one tenant's
		// device the boot policy of another tenant's node, so refuse: the
		// device falls back to its local boot order, which is recoverable,
		// where a wrong netboot decision is not.
		slog.Error("hardware serial resolves to nodes in MORE THAN ONE account; refusing to guess",
			"serial", hardwareSerial, "count", len(models))
		return nil, app_errors.ErrHardwareSerialAmbiguous
	}
	if len(models) > 1 {
		slog.Warn("multiple nodes share a hardware serial; resolved by EEPROM readback, exact match, then newest",
			"serial", hardwareSerial, "count", len(models), "node_id", resolved.ID)
	}
	return resolved, nil
}

// isShortSerial reports whether a serial is the short (low-word) form — shorter
// than the full 16-hex serial the agent reports — so a suffix match is safe.
func isShortSerial(serial string) bool {
	return len(serial) > 0 && len(serial) < 16
}

// minSuffixSerialLen is the shortest query serial that may be suffix-matched.
// The WebUSB flasher harvests an 8-hex USB descriptor serial, so 8 is the real
// low-word width; anything shorter is not a serial anyone legitimately has, and
// treating it as one turns a suffix match into an enumeration primitive.
const minSuffixSerialLen = 8

// isSuffixMatchableSerial reports whether a QUERY serial is short enough to be
// the low-word form and long enough not to over-match. The account-scoped
// sibling applies the same floor to the STORED value, for the same reason.
func isSuffixMatchableSerial(serial string) bool {
	return len(serial) >= minSuffixSerialLen && isShortSerial(serial)
}

// ListByExactSerial returns every node (across all accounts) whose stored
// serial equals the normalized serial, case-insensitively and EXACTLY — no
// suffix fuzziness, so another account's short placeholder for a different
// physical device can never satisfy a full-serial guard query.
func (r *NodeRepository) ListByExactSerial(ctx context.Context, normalizedSerial string) ([]*node.Node, error) {
	var models []NodeModel
	result := r.db.WithContext(ctx).
		Where("lower(fingerprint->>'hardware_serial') = ?", normalizedSerial).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}
	nodes := make([]*node.Node, 0, len(models))
	for i := range models {
		nodes = append(nodes, r.toDomain(&models[i]))
	}
	return nodes, nil
}

// LowWordHolderInAnotherAccount returns another account's node whose stored
// serial collides with this one by its low word (see the port). The two shapes
// are written out rather than derived, because the guard that calls this is
// what keeps one account's row off the boot path another account's device
// asks for: a full serial collides with another full serial sharing its low
// word and with a short stamp it ends in; a short stamp collides with a full
// serial ending in it. Only account_id and id are selected, so no credential
// column is read, let alone decrypted.
func (r *NodeRepository) LowWordHolderInAnotherAccount(ctx context.Context, accountID uuid.UUID, normalizedSerial string) (*repository.SerialHolder, error) {
	const stored = "lower(fingerprint->>'hardware_serial')"
	query := r.db.WithContext(ctx).
		Table("nodes").
		Select("account_id, id").
		Where("account_id <> ?", accountID).
		Where(stored+" <> ?", normalizedSerial)
	switch {
	case node.LowWord(normalizedSerial) != "":
		query = query.Where("("+stored+" ~ '^[0-9a-f]{16}$' AND right("+stored+", ?) = ?)"+
			" OR ("+stored+" ~ '^[0-9a-f]{8,15}$' AND right(?, length("+stored+")) = "+stored+")",
			node.LowWordLen, node.LowWord(normalizedSerial), normalizedSerial)
	case isSuffixMatchableSerial(normalizedSerial):
		query = query.Where(stored+" ~ '^[0-9a-f]{16}$' AND right("+stored+", ?) = ?",
			len(normalizedSerial), normalizedSerial)
	default:
		return nil, nil
	}
	var rows []struct {
		AccountID uuid.UUID
		ID        uuid.UUID
	}
	if err := query.Limit(1).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &repository.SerialHolder{AccountID: rows[0].AccountID, NodeID: rows[0].ID}, nil
}

// SerialBindingScopes reports, per NORMALIZED serial, whether the asking account
// and/or some other account carries it — in ONE query, and without hydrating a
// single row.
//
// Two properties are load-bearing and easy to lose:
//
//   - No node is materialized, so nothing is decrypted. ListByExactSerial cannot
//     serve a read path for exactly that reason (see the port's comment).
//   - An EMPTY input must not reach the database. GORM renders an empty IN as
//     `IN (NULL)`, which is a sequential scan of `nodes` returning nothing — the
//     worst outcome for the common case of an account whose tombstones all carry
//     short serials or none.
//
// The comparison matches the expression index created in Setup()
// (lower(fingerprint->>'hardware_serial')), so this is an index scan.
func (r *NodeRepository) SerialBindingScopes(ctx context.Context, accountID uuid.UUID, normalizedSerials []string) (map[string]repository.SerialBindingScope, error) {
	scopes := make(map[string]repository.SerialBindingScope, len(normalizedSerials))
	if len(normalizedSerials) == 0 {
		return scopes, nil
	}
	var rows []struct {
		Serial           string
		InAccount        bool
		InAnotherAccount bool
	}
	result := r.db.WithContext(ctx).
		Table("nodes").
		Select("lower(fingerprint->>'hardware_serial') AS serial, "+
			"bool_or(account_id = ?) AS in_account, "+
			"bool_or(account_id <> ?) AS in_another_account", accountID, accountID).
		Where("lower(fingerprint->>'hardware_serial') IN ?", normalizedSerials).
		// The full expression, never Group("1"): GORM treats a name made only of
		// identifier characters as a column and QUOTES it, so "1" becomes
		// GROUP BY "1" — a reference to a column named 1, not the ordinal — and
		// Postgres refuses the statement. The caller fails OPEN on that error,
		// so the flag would silently never render. Pinned by the DSN-gated
		// TestSerialBindingScopesGroupsByTheNormalizedSerial.
		Group("lower(fingerprint->>'hardware_serial')").
		Scan(&rows)
	if result.Error != nil {
		return nil, result.Error
	}
	for _, row := range rows {
		scopes[row.Serial] = repository.SerialBindingScope{
			InAccount:        row.InAccount,
			InAnotherAccount: row.InAnotherAccount,
		}
	}
	return scopes, nil
}

// SerialHoldersInAccount reports, per NORMALIZED serial, the node of accountID
// that carries it — newest first when several do. The WHERE names the account,
// so another tenant's rows never reach this process, and only id and name are
// selected, so nothing is decrypted. An empty input does not reach the database
// (see SerialBindingScopes).
func (r *NodeRepository) SerialHoldersInAccount(ctx context.Context, accountID uuid.UUID, normalizedSerials []string) (map[string]repository.AccountSerialHolder, error) {
	holders := make(map[string]repository.AccountSerialHolder, len(normalizedSerials))
	if len(normalizedSerials) == 0 {
		return holders, nil
	}
	var rows []struct {
		Serial string
		ID     uuid.UUID
		Name   string
	}
	result := r.db.WithContext(ctx).
		Table("nodes").
		Select("lower(fingerprint->>'hardware_serial') AS serial, id, name").
		Where("account_id = ? AND lower(fingerprint->>'hardware_serial') IN ?", accountID, normalizedSerials).
		Order("created_at DESC").
		Scan(&rows)
	if result.Error != nil {
		return nil, result.Error
	}
	for _, row := range rows {
		if _, seen := holders[row.Serial]; seen {
			continue
		}
		holders[row.Serial] = repository.AccountSerialHolder{NodeID: row.ID, Name: row.Name}
	}
	return holders, nil
}

// ReleaseUnclaimedSerialLeases releases the serial of never-admitted nodes
// nothing has been seen from since the cutoff.
//
// A CTE snapshots the serial BEFORE the update — RETURNING sees the new row,
// where fingerprint is already NULL — and FOR UPDATE SKIP LOCKED keeps a
// connect landing on the same row from waiting on the sweep. The eligibility
// is deliberately narrow and lives in this one statement: awaiting_first_connect
// (a WebUSB/SSH placeholder) or pending_approval (a master-token adopt the
// operator has not accepted), a full 16-hex serial (the only shape the
// cross-account guard protects, so the only shape a squat can use), and no
// sign of life. An admitted node — approved and past its first connect — is
// never eligible, however long it has been offline; pinned by the DSN-gated
// TestReleaseUnclaimedSerialLeasesEligibility.
//
// "Admitted" is the admittedScope predicate (approved, status outside the
// pre-connect statuses), stated in the statement itself rather than inferred
// from awaiting_first_connect: a node restored from an admitted tombstone is
// approved and offline AND awaiting its first connect (so its device can
// rebind the full fingerprint), and clearing its serial would undo the one
// thing the restore put back for HTTP-boot to find it by.
func (r *NodeRepository) ReleaseUnclaimedSerialLeases(ctx context.Context, unseenSince time.Time) ([]repository.ReleasedSerialLease, error) {
	var rows []struct {
		ID             uuid.UUID
		AccountID      uuid.UUID
		HardwareSerial string
	}
	result := r.db.WithContext(ctx).Raw(`
		WITH due AS (
			SELECT id, account_id, lower(fingerprint->>'hardware_serial') AS hardware_serial
			FROM nodes
			WHERE (awaiting_first_connect = true OR approval_status = ?)
			  AND NOT (approval_status = ? AND status NOT IN ?)
			  AND lower(fingerprint->>'hardware_serial') ~ '^[0-9a-f]{16}$'
			  AND GREATEST(created_at, coalesce(last_seen_at, created_at), coalesce(last_heartbeat, created_at)) < ?
			FOR UPDATE SKIP LOCKED
		)
		UPDATE nodes n
		SET fingerprint = NULL, updated_at = now()
		FROM due
		WHERE n.id = due.id
		RETURNING due.id, due.account_id, due.hardware_serial`,
		string(node.ApprovalStatusPendingApproval),
		string(node.ApprovalStatusApproved), preConnectStatuses(),
		unseenSince).Scan(&rows)
	if result.Error != nil {
		return nil, result.Error
	}
	out := make([]repository.ReleasedSerialLease, 0, len(rows))
	for _, row := range rows {
		out = append(out, repository.ReleasedSerialLease{NodeID: row.ID, AccountID: row.AccountID, HardwareSerial: row.HardwareSerial})
	}
	return out, nil
}

// GetAwaitingFirstConnectBySerialSuffix finds a WebUSB placeholder (awaiting its
// first connect, stamped with the SHORT serial) whose stored serial is the low
// word of the connecting device's FULL serial. Lets a device that adopts via any
// path reconcile into the placeholder instead of spawning a duplicate node.
func (r *NodeRepository) GetAwaitingFirstConnectBySerialSuffix(ctx context.Context, accountID uuid.UUID, fullSerial string) (*node.Node, error) {
	var model NodeModel
	// right()-comparison, NOT LIKE: the stored serial would otherwise act as a
	// pattern, so a placeholder stamped with '%' or '_' could match any device.
	result := r.db.WithContext(ctx).
		Where(
			"account_id = ? AND awaiting_first_connect = true AND coalesce(fingerprint->>'hardware_serial', '') <> '' AND right(?, char_length(fingerprint->>'hardware_serial')) = (fingerprint->>'hardware_serial')",
			accountID, fullSerial,
		).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNodeNotFound
		}
		return nil, result.Error
	}
	return r.toDomain(&model), nil
}

// IncrementBootAttempt atomically counts one withheld netboot attempt.
//   - within `debounce` of the last counted attempt -> no-op (collapses a single
//     boot's duplicate/retry requests), returns the current count with counted=false;
//   - older than `window` -> starts a fresh series at 1 (a real crash-loop reboots
//     within the window; an occasional reboot resets the series);
//   - otherwise increments.
//
// Done as a single UPDATE ... RETURNING so concurrent requests for one serial cannot
// lose updates.
func (r *NodeRepository) IncrementBootAttempt(ctx context.Context, nodeID uuid.UUID, window, debounce time.Duration) (int, bool, error) {
	now := time.Now()
	windowCutoff := now.Add(-window)
	debounceCutoff := now.Add(-debounce)

	var count int
	res := r.db.WithContext(ctx).Raw(`
		UPDATE nodes
		SET boot_attempt_count = CASE
				WHEN last_boot_attempt_at IS NULL OR last_boot_attempt_at < ? THEN 1
				ELSE boot_attempt_count + 1 END,
			last_boot_attempt_at = ?,
			updated_at = ?
		WHERE id = ? AND (last_boot_attempt_at IS NULL OR last_boot_attempt_at <= ?)
		RETURNING boot_attempt_count`,
		windowCutoff, now, now, nodeID, debounceCutoff).Scan(&count)
	if res.Error != nil {
		return 0, false, res.Error
	}
	if res.RowsAffected == 0 {
		// Debounced (or node gone): return the current stored count unchanged.
		var cur int
		if err := r.db.WithContext(ctx).Model(&NodeModel{}).
			Where("id = ?", nodeID).Select("boot_attempt_count").Scan(&cur).Error; err != nil {
			return 0, false, err
		}
		return cur, false, nil
	}
	return count, true, nil
}

// SetBootFailsafe records that the failsafe served recovery, and (one-shot) resets
// the counter to `resetCountTo` so the NEXT boot withholds again and gives the OS
// another chance. The boot_failsafe_at flag stays set until a healthy OS boot or an
// operator acknowledge clears it.
func (r *NodeRepository) SetBootFailsafe(ctx context.Context, nodeID uuid.UUID, at time.Time, resetCountTo int) error {
	result := r.db.WithContext(ctx).Model(&NodeModel{}).
		Where("id = ?", nodeID).
		Updates(map[string]interface{}{
			"boot_failsafe_at":   at,
			"boot_attempt_count": resetCountTo,
			"updated_at":         time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrNodeNotFound
	}
	return nil
}

// ResetBootFailsafe clears the failsafe counter and flag (a confirmed OS boot, an
// operator acknowledge, or an operator boot-intent action).
func (r *NodeRepository) ResetBootFailsafe(ctx context.Context, nodeID uuid.UUID) error {
	result := r.db.WithContext(ctx).Model(&NodeModel{}).
		Where("id = ?", nodeID).
		Updates(map[string]interface{}{
			"boot_attempt_count": 0,
			"boot_failsafe_at":   nil,
			"updated_at":         time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrNodeNotFound
	}
	return nil
}

// UpdateApprovalStatus updates the approval status of a node
// UpdateApprovalStatus is never an admission: approving lands only on a row
// still in a pre-connect status, where approval does not admit it.
// Approving a row in any other status is its admission and is refused with
// *node.AdmissionRequiredError (ApproveUnderDeviceCap is that write) — the
// status can move between the caller's read and this write.
func (r *NodeRepository) UpdateApprovalStatus(ctx context.Context, nodeID uuid.UUID, status string) error {
	db := r.db.WithContext(ctx)
	if status != string(node.ApprovalStatusApproved) {
		return r.updateApprovalStatusIn(db, nodeID, status)
	}
	result := db.Model(&NodeModel{}).
		Where("id = ? AND status IN ?", nodeID, preConnectStatuses()).
		Updates(map[string]interface{}{
			"approval_status": status,
			"updated_at":      time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return r.admissionRefusal(db, nodeID, uuid.Nil, "")
	}
	return nil
}

func (r *NodeRepository) updateApprovalStatusIn(db *gorm.DB, nodeID uuid.UUID, status string) error {
	result := db.
		Model(&NodeModel{}).
		Where("id = ?", nodeID).
		Updates(map[string]interface{}{
			"approval_status": status,
			"updated_at":      time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrNodeNotFound
	}

	return nil
}

// GetPendingApprovalNodes retrieves all nodes pending approval for an account
func (r *NodeRepository) GetPendingApprovalNodes(ctx context.Context, accountID uuid.UUID) ([]node.Node, error) {
	var models []NodeModel
	result := r.db.WithContext(ctx).
		Where("account_id = ? AND approval_status = ?", accountID, string(node.ApprovalStatusPendingApproval)).
		Order("created_at DESC").
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	nodes := make([]node.Node, len(models))
	for i, model := range models {
		nodes[i] = *r.toDomain(&model)
	}

	return nodes, nil
}
