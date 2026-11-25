package sql

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// commandCipherPrefix marks a field this package encrypted. Rows written before
// encryption existed carry no prefix and are returned as-is (legacy plaintext);
// repository Setup() backfills them to ciphertext idempotently.
const commandCipherPrefix = "encv1:"

// commandPlaintextKeys are the ONLY payload keys deliberately left readable at
// rest. Everything else is encrypted regardless of name — see
// encryptCommandPayload.
//
// SecretDerivedParamsKey holds param NAMES, not values, and is what
// results.RedactCommandPayload consults to decide what to hide. Keeping it
// plaintext means redaction still works on a row whose values cannot be
// decrypted (a half-rotated key), which is exactly when getting it wrong would
// matter most.
// WorkflowBlockIDKey is likewise names-not-values: a graph block id, needed by
// the engine's lost-dispatch reconciliation even when the row's values sit on
// an old key.
var commandPlaintextKeys = map[string]bool{
	results.SecretDerivedParamsKey: true,
	results.WorkflowBlockIDKey:     true,
}

// encryptCommandDetails seals the free-text details column.
//
// It is a whole-value ciphertext carrying the same commandCipherPrefix as the
// payload's values, rather than a bare ciphertext in a `*_enc`-marked column,
// for one reason: that prefix is what lets `rewrapNodeCommands` find it. The
// rewrap walks the row's raw text for `encv1:` tokens and does not know or care
// which column they came from, so a prefixed value joins the existing rotation
// target instead of needing a twelfth one.
//
// preservePrefixed has the same meaning as in encryptCommandPayload: the
// BACKFILL sweeps raw rows where the prefix marks our own ciphertext, while the
// write path seals a DOMAIN value, where a leading "encv1:" is caller text that
// happens to start with the token.
//
// Returns ok=false when the value cannot be sealed, so the caller drops it
// rather than persisting it in the clear.
func encryptCommandDetails(details string, enc commandFieldEncryptor, preservePrefixed bool) (string, bool) {
	if details == "" || enc == nil {
		return details, true
	}
	if preservePrefixed && strings.HasPrefix(details, commandCipherPrefix) {
		return details, true
	}
	ciphertext, err := enc.Encrypt(details)
	if err != nil {
		return "", false
	}
	return commandCipherPrefix + ciphertext, true
}

// decryptCommandDetails reverses encryptCommandDetails. An unprefixed value is
// legacy plaintext and passes through; an undecryptable one becomes empty
// rather than surfacing ciphertext, which an operator would read as the failure
// itself.
func decryptCommandDetails(details string, enc commandFieldEncryptor) string {
	if enc == nil || !strings.HasPrefix(details, commandCipherPrefix) {
		return details
	}
	plain, err := enc.Decrypt(strings.TrimPrefix(details, commandCipherPrefix))
	if err != nil {
		return ""
	}
	return plain
}

// commandFieldEncryptor is the subset of the crypto encryptor these conversions
// need. A nil encryptor disables encryption (values are stored as before),
// which keeps the model usable in tests.
type commandFieldEncryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

// encryptCommandPayload returns a copy of the payload with EVERY string value
// encrypted, at any depth. It used to encrypt a fixed list of secret-bearing
// keys, which is inherently incomplete: a {{secrets.*}} reference resolves into
// whatever param the operator authored it into — download_file.dest_path, an
// install_packages spec pointing at a credentialed repo — and those reached the
// column in the clear. Key names stay readable, so a row
// is still recognisable in psql; only values are opaque.
//
// Failure is ATOMIC: if any value cannot be encrypted the whole payload is
// rejected (error, nil map) instead of the previous per-field drop, which could
// silently strip a required param like target_device from a command that then
// ran against the wrong disk.
//
// preservePrefixed decides what an "encv1:"-prefixed value means, and getting
// it wrong stored legitimate prefixed values in the clear. The BACKFILL sweeps raw rows where the prefix marks
// ciphertext this package already wrote (possibly under a dropped key, which
// must be left verbatim for the rewrap machinery — never re-wrapped, or the
// coverage check reads it as clean while the inner value is still opaque), so
// it passes true. The WRITE paths encrypt DOMAIN values, which never carry our
// own ciphertext — ToDomain drops anything undecryptable — so a prefixed
// string there is caller plaintext that happens to start with the token
// (a file whose bytes begin "encv1:", a script echoing one); skipping it on
// the prefix alone stored it in the clear AND made every later read drop it
// as undecryptable. They pass false and encrypt everything.
func encryptCommandPayload(payload map[string]interface{}, enc commandFieldEncryptor, preservePrefixed bool) (map[string]interface{}, error) {
	if payload == nil || enc == nil {
		return payload, nil
	}
	out := make(map[string]interface{}, len(payload))
	for k, v := range payload {
		if commandPlaintextKeys[k] {
			out[k] = v
			continue
		}
		encrypted, err := encryptCommandValue(v, enc, preservePrefixed)
		if err != nil {
			return nil, fmt.Errorf("payload field %q could not be encrypted: %w", k, err)
		}
		out[k] = encrypted
	}
	return out, nil
}

// encryptCommandValue encrypts every string reachable from v. Non-strings
// (numbers, booleans, nulls) are structural and pass through: the resolver only
// ever substitutes secret material into strings.
func encryptCommandValue(v interface{}, enc commandFieldEncryptor, preservePrefixed bool) (interface{}, error) {
	switch val := v.(type) {
	case string:
		if val == "" || (preservePrefixed && strings.HasPrefix(val, commandCipherPrefix)) {
			return val, nil
		}
		ciphertext, err := enc.Encrypt(val)
		if err != nil {
			return nil, err
		}
		return commandCipherPrefix + ciphertext, nil
	case []interface{}:
		out := make([]interface{}, len(val))
		for i := range val {
			item, err := encryptCommandValue(val[i], enc, preservePrefixed)
			if err != nil {
				return nil, err
			}
			out[i] = item
		}
		return out, nil
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, item := range val {
			encrypted, err := encryptCommandValue(item, enc, preservePrefixed)
			if err != nil {
				return nil, err
			}
			out[k] = encrypted
		}
		return out, nil
	default:
		return v, nil
	}
}

// decryptCommandPayload reverses encryptCommandPayload. Unprefixed values are
// legacy plaintext (written before encryption, or before it covered every
// field) and pass through untouched, so old rows stay readable.
//
// It returns the names of the fields it had to drop. Dropping them is right for
// DISPLAY — ciphertext tells an operator nothing — and catastrophic for
// dispatch, so the caller records the names on the domain command and the
// dispatch path refuses it. Silently sending a smaller apply_access_config is
// how a revocation becomes a grant.
func decryptCommandPayload(payload map[string]interface{}, enc commandFieldEncryptor) (map[string]interface{}, []string) {
	if payload == nil || enc == nil {
		return payload, nil
	}
	var dropped []string
	for k, v := range payload {
		if commandPlaintextKeys[k] {
			continue
		}
		decrypted, ok := decryptCommandValue(v, enc)
		if !ok {
			// Undecryptable (e.g. the key was rotated): surface the absence
			// rather than the ciphertext, which would be meaningless to a
			// caller and misleading in the UI.
			delete(payload, k)
			dropped = append(dropped, k)
			continue
		}
		payload[k] = decrypted
	}
	sort.Strings(dropped)
	return payload, dropped
}

// decryptCommandValue returns the decrypted value and whether it survived. A
// nested value that fails to decrypt collapses the whole branch to ok=false, so
// the caller drops the key rather than handing back a half-decrypted structure.
func decryptCommandValue(v interface{}, enc commandFieldEncryptor) (interface{}, bool) {
	switch val := v.(type) {
	case string:
		if !strings.HasPrefix(val, commandCipherPrefix) {
			return val, true
		}
		plain, err := enc.Decrypt(strings.TrimPrefix(val, commandCipherPrefix))
		if err != nil {
			return nil, false
		}
		return plain, true
	case []interface{}:
		out := make([]interface{}, len(val))
		for i := range val {
			item, ok := decryptCommandValue(val[i], enc)
			if !ok {
				return nil, false
			}
			out[i] = item
		}
		return out, true
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, item := range val {
			decrypted, ok := decryptCommandValue(item, enc)
			if !ok {
				return nil, false
			}
			out[k] = decrypted
		}
		return out, true
	default:
		return v, true
	}
}

// encryptCommandResult encrypts every agent-reported output value. Unlike the
// payload there is no field whitelist: read_file/run_script return whatever
// they read, so all of it is treated as secret-bearing.
func encryptCommandResult(result map[string]string, enc commandFieldEncryptor, preservePrefixed bool) (map[string]string, error) {
	if result == nil || enc == nil {
		return result, nil
	}
	out := make(map[string]string, len(result))
	for k, v := range result {
		if v == "" || (preservePrefixed && strings.HasPrefix(v, commandCipherPrefix)) {
			out[k] = v
			continue
		}
		ciphertext, err := enc.Encrypt(v)
		if err != nil {
			return nil, fmt.Errorf("result field %q could not be encrypted: %w", k, err)
		}
		out[k] = commandCipherPrefix + ciphertext
	}
	return out, nil
}

func decryptCommandResult(result map[string]string, enc commandFieldEncryptor) map[string]string {
	if result == nil || enc == nil {
		return result
	}
	for k, v := range result {
		if !strings.HasPrefix(v, commandCipherPrefix) {
			continue
		}
		plain, err := enc.Decrypt(strings.TrimPrefix(v, commandCipherPrefix))
		if err != nil {
			delete(result, k)
			continue
		}
		result[k] = plain
	}
	return result
}

// NodeCommandModel is the SQL representation of a node command
type NodeCommandModel struct {
	ID uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null;index"`
	// Two composite indexes serve the two time-ordered reads the single-column
	// ones each cover only half of: (node_id, created_at) for "this node's
	// recent commands" in the node sidebar, and (account_id, created_at) for
	// the fleet activity log, which pages the whole account newest-first.
	NodeID                  uuid.UUID  `gorm:"column:node_id;type:uuid;index;index:idx_node_commands_node_created,priority:1;not null"`
	AccountID               uuid.UUID  `gorm:"column:account_id;type:uuid;index;index:idx_node_commands_account_created,priority:1;not null"`
	Slug                    string     `gorm:"column:slug;size:100;index;not null"`
	Payload                 *string    `gorm:"column:payload;type:jsonb"` // Nullable JSON storage
	Status                  string     `gorm:"column:status;size:50;index;not null;default:'registered'"`
	Progress                int        `gorm:"column:progress;default:0"`
	Details                 *string    `gorm:"column:details;type:text"`                          // Nullable
	Result                  []byte     `gorm:"column:result;type:jsonb"`                          // Structured agent outputs (workflow blocks)
	WorkflowExecutionNodeID *uuid.UUID `gorm:"column:workflow_execution_node_id;type:uuid;index"` // Nullable foreign key to workflow_execution_nodes
	// Queue columns. Queueable commands wait in "registered" for an offline node
	// and deliver on reconnect; SupersedeKey lets a newer command replace an
	// unsent one for the same target; ExpiresAt bounds how long a command may
	// wait; Attempts counts delivery tries.
	Queueable    bool       `gorm:"column:queueable;not null;default:false"`
	SupersedeKey *string    `gorm:"column:supersede_key;size:200;index"`
	ExpiresAt    *time.Time `gorm:"column:expires_at;index"`
	Attempts     int        `gorm:"column:attempts;not null;default:0"`
	CreatedAt    time.Time  `gorm:"column:created_at;index;index:idx_node_commands_node_created,priority:2;index:idx_node_commands_account_created,priority:2;not null;default:now()"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for this model
func (NodeCommandModel) TableName() string {
	return "node_commands"
}

// ToDomain converts the SQL model to a domain model, decrypting the
// secret-bearing fields.
func (m *NodeCommandModel) ToDomain(enc commandFieldEncryptor) *node_command.NodeCommand {
	domainCmd := &node_command.NodeCommand{
		ID:        m.ID,
		NodeID:    m.NodeID,
		AccountID: m.AccountID,
		Slug:      m.Slug,
		Status:    node_command.NodeCommandStatus(m.Status),
		Progress:  m.Progress,
		Queueable: m.Queueable,
		ExpiresAt: m.ExpiresAt,
		Attempts:  m.Attempts,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,

		WorkflowExecutionNodeID: m.WorkflowExecutionNodeID,
	}
	if m.SupersedeKey != nil {
		domainCmd.SupersedeKey = *m.SupersedeKey
	}

	// Parse Payload from JSON
	if m.Payload != nil && *m.Payload != "" {
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(*m.Payload), &payload); err == nil {
			domainCmd.Payload, domainCmd.UndecryptablePayloadKeys = decryptCommandPayload(payload, enc)
		} else {
			domainCmd.Payload = make(map[string]interface{})
		}
	} else {
		domainCmd.Payload = make(map[string]interface{})
	}

	// Details is the agent's own prose about a failure, and an agent echoes
	// params back — a rejected value, a transport error, a tool's stderr — so a
	// resolved {{secrets.*}} reaches it as readily as it reaches an output.
	// Sealed at rest for that reason; see encryptCommandDetails.
	if m.Details != nil {
		domainCmd.Details = decryptCommandDetails(*m.Details, enc)
	}

	// Parse Result from JSON
	if len(m.Result) > 0 {
		var result map[string]string
		if err := json.Unmarshal(m.Result, &result); err == nil {
			domainCmd.Result = decryptCommandResult(result, enc)
		}
	}

	return domainCmd
}

// FromDomain updates the SQL model from a domain model, encrypting the
// secret-bearing fields on the way to storage.
func (m *NodeCommandModel) FromDomain(cmd *node_command.NodeCommand, enc commandFieldEncryptor) {
	m.ID = cmd.ID
	m.NodeID = cmd.NodeID
	m.AccountID = cmd.AccountID
	m.Slug = cmd.Slug
	m.Status = string(cmd.Status)
	m.Progress = cmd.Progress
	m.Queueable = cmd.Queueable
	m.ExpiresAt = cmd.ExpiresAt
	m.Attempts = cmd.Attempts
	if cmd.SupersedeKey != "" {
		m.SupersedeKey = &cmd.SupersedeKey
	} else {
		m.SupersedeKey = nil
	}
	m.CreatedAt = cmd.CreatedAt
	m.UpdatedAt = cmd.UpdatedAt

	// Serialize Payload to JSON. Encryption failure drops the payload entirely
	// rather than storing it in the clear — a command that arrives without its
	// params fails loudly; one stored in plaintext fails silently, forever.
	m.Payload = nil
	if len(cmd.Payload) > 0 {
		encrypted, err := encryptCommandPayload(cmd.Payload, enc, false)
		if err != nil {
			slog.Error("dropping node-command payload it could not encrypt", "command_id", cmd.ID, "slug", cmd.Slug, "error", err)
		} else if payloadJSON, err := json.Marshal(encrypted); err == nil {
			payloadStr := string(payloadJSON)
			m.Payload = &payloadStr
		}
	}

	// Details, sealed. A failure to encrypt drops the text rather than storing
	// it plainly: the STATUS still says what happened, which is the part a
	// caller acts on.
	m.Details = nil
	if cmd.Details != "" {
		if sealed, ok := encryptCommandDetails(cmd.Details, enc, false); ok {
			m.Details = &sealed
		} else {
			slog.Error("dropping node-command details it could not encrypt", "command_id", cmd.ID, "slug", cmd.Slug)
		}
	}

	// Serialize Result to JSON
	m.Result = nil
	if len(cmd.Result) > 0 {
		encrypted, err := encryptCommandResult(cmd.Result, enc, false)
		if err != nil {
			slog.Error("dropping node-command result it could not encrypt", "command_id", cmd.ID, "slug", cmd.Slug, "error", err)
		} else if resultJSON, err := json.Marshal(encrypted); err == nil {
			m.Result = resultJSON
		}
	}
}
