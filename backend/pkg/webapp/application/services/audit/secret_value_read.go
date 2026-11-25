package audit

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// SecretValueReadScope names WHAT a caller was served plaintext for, so the
// trail distinguishes "listed every value in the account" from "opened one
// secret".
type SecretValueReadScope string

const (
	// ScopeAccountSecrets is the whole account's secret list.
	ScopeAccountSecrets SecretValueReadScope = "account_secrets"
	// ScopeSecret is one secret, by id.
	ScopeSecret SecretValueReadScope = "secret"
	// ScopeOUSecrets is the effective secret set of one organizational unit.
	ScopeOUSecrets SecretValueReadScope = "ou_effective_secrets"
	// ScopeNodeSecrets is the effective secret set of one node.
	ScopeNodeSecrets SecretValueReadScope = "node_effective_secrets"
	// ScopeNodeAttributes is one node's attributes, which are secrets.
	ScopeNodeAttributes SecretValueReadScope = "node_attributes"
	// ScopeCommandPayload is a device command's payload or result, where the
	// engine substituted resolved secret material.
	ScopeCommandPayload SecretValueReadScope = "command_payload"
	// ScopeRunState is a workflow run's block outputs and command history, the
	// same bytes under a different name.
	ScopeRunState SecretValueReadScope = "run_state"
)

// RecordSecretValuesRead appends the "this caller was served plaintext" row.
//
// It is called from the ONE place each handler decides the question — the
// permission helper itself — rather than from every endpoint, so an endpoint
// added later is covered by construction. That placement is deliberate: the
// alternative is a list of call sites, and a list of call sites is what the
// original gap was.
//
// subjectID is the node, unit or secret the read was scoped to, empty for an
// account-wide list. Nothing else travels: no names, no values. The names alone
// would be a reconnaissance map (a secret's name IS its {{secrets.<name>}}
// identifier), and audit_events is on no rewrap list.
func (r *Recorder) RecordSecretValuesRead(
	ctx context.Context,
	actor Actor,
	scope SecretValueReadScope,
	targetType, subjectID string,
) {
	if r == nil {
		return
	}
	r.RecordDetail(ctx, actor, audit_event.ActionSecretValuesRead, targetType, subjectID,
		fmt.Sprintf("plaintext served (scope %s)", scope))
}

// SecretValueReader answers "may this caller see plaintext" and records the
// answer when it is yes.
//
// Handlers hold one of these instead of an enforcer-plus-recorder pair, so the
// permission check and the trail row cannot be separated by a later edit.
type SecretValueReader struct {
	can      func(ctx context.Context, accountID, userID uuid.UUID) bool
	recorder *Recorder
}

// NewSecretValueReader wires the check to the recording.
func NewSecretValueReader(
	can func(ctx context.Context, accountID, userID uuid.UUID) bool,
	recorder *Recorder,
) *SecretValueReader {
	if can == nil {
		panic("audit.SecretValueReader requires a permission check")
	}
	if recorder == nil {
		panic("audit.SecretValueReader requires an audit recorder")
	}
	return &SecretValueReader{can: can, recorder: recorder}
}

// Allowed reports whether the caller may see plaintext, recording the access
// when they may. A refusal writes nothing: a caller who saw nothing did not read
// anything, and a row per refused request would bury the reads that matter.
func (s *SecretValueReader) Allowed(
	ctx context.Context,
	accountID, userID uuid.UUID,
	scope SecretValueReadScope,
	targetType, subjectID string,
) bool {
	if s == nil {
		return false
	}
	if !s.can(ctx, accountID, userID) {
		return false
	}
	s.recorder.RecordSecretValuesRead(ctx, UserActor(accountID, userID), scope, targetType, subjectID)
	return true
}
