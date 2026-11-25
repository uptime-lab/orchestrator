package sql

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

// UtilityAssignmentModel is the GORM row for one utility × one target × one
// config. The OU target is a SET in utility_assignment_ous (the access-entry
// model); account_wide / node_id stay columns. Config carries no secret
// material by doctrine (params reference variables/attributes instead), so
// the column is plaintext and deliberately NOT on the rewrap list.
type UtilityAssignmentModel struct {
	ID          uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID   uuid.UUID `gorm:"column:account_id;type:uuid;index;not null"`
	UtilityID   string    `gorm:"column:utility_id;size:64;not null"`
	AccountWide bool      `gorm:"column:account_wide;not null;default:false"`
	// OUID is the LEGACY single-unit column, superseded by the
	// utility_assignment_ous join table. Kept only so Setup() can backfill
	// the join from pre-existing rows; never written or read after that.
	OUID      *uuid.UUID             `gorm:"column:ou_id;type:uuid;index"`
	NodeID    *uuid.UUID             `gorm:"column:node_id;type:uuid;index"`
	Config    map[string]interface{} `gorm:"column:config;serializer:json"`
	Enabled   bool                   `gorm:"column:enabled;not null;default:true"`
	CreatedAt time.Time              `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt time.Time              `gorm:"column:updated_at;not null;default:now()"`
}

func (UtilityAssignmentModel) TableName() string { return "utility_assignments" }

// toDomain maps the row WITHOUT OUIDs — the repository hydrates them from the
// join table.
func (m *UtilityAssignmentModel) toDomain() *utility.Assignment {
	return &utility.Assignment{
		ID:          m.ID,
		AccountID:   m.AccountID,
		UtilityID:   m.UtilityID,
		AccountWide: m.AccountWide,
		NodeID:      m.NodeID,
		Config:      m.Config,
		Enabled:     m.Enabled,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

// fromDomain maps the domain entity WITHOUT the legacy ou_id (never written
// again) — OUIDs live in the join table.
func (m *UtilityAssignmentModel) fromDomain(a *utility.Assignment) {
	m.ID = a.ID
	m.AccountID = a.AccountID
	m.UtilityID = a.UtilityID
	m.AccountWide = a.AccountWide
	m.NodeID = a.NodeID
	m.Config = a.Config
	m.Enabled = a.Enabled
	m.CreatedAt = a.CreatedAt
	m.UpdatedAt = a.UpdatedAt
}

// UtilityAssignmentOUModel is the join row scoping an assignment to a unit.
// Composite PK makes re-assignment idempotent; account_id scopes deletes
// (and is what an account purge reads).
type UtilityAssignmentOUModel struct {
	AssignmentID uuid.UUID `gorm:"column:assignment_id;type:uuid;primaryKey;not null"`
	OUID         uuid.UUID `gorm:"column:ou_id;type:uuid;primaryKey;not null;index"`
	AccountID    uuid.UUID `gorm:"column:account_id;type:uuid;index;not null"`
}

func (UtilityAssignmentOUModel) TableName() string { return "utility_assignment_ous" }

// NodeUtilityStateModel is the per-(node, utility) ladder rung. applied_hash
// is written only by the settle path from the platform's own dispatch payload.
type NodeUtilityStateModel struct {
	ID          uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID   uuid.UUID `gorm:"column:account_id;type:uuid;index;not null"`
	NodeID      uuid.UUID `gorm:"column:node_id;type:uuid;index;not null;uniqueIndex:uq_node_utility,priority:1"`
	UtilityID   string    `gorm:"column:utility_id;size:64;not null;uniqueIndex:uq_node_utility,priority:2"`
	DesiredHash string    `gorm:"column:desired_hash;size:64;not null;default:''"`
	// The pre-resolution source hash the OU rollup compares against; empty on
	// rows last applied before the column existed (see the domain field).
	DesiredSourceHash string `gorm:"column:desired_source_hash;size:64;not null;default:''"`
	AppliedHash       string `gorm:"column:applied_hash;size:64;not null;default:''"`
	LastError         string `gorm:"column:last_error;not null;default:''"`
	// DriftFailures counts CONSECUTIVE failed applies of the SAME desired
	// source hash — the reconcile loop's 3-strike stand-down key. Reset by a
	// successful apply and by any config change (SetDesired with a new source
	// hash), so recovery is an edit or the manual apply, the access-drift
	// precedent.
	DriftFailures int       `gorm:"column:drift_failures;not null;default:0"`
	CreatedAt     time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt     time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (NodeUtilityStateModel) TableName() string { return "node_utility_states" }

func (m *NodeUtilityStateModel) toDomain() utility.NodeState {
	return utility.NodeState{
		ID:                m.ID,
		AccountID:         m.AccountID,
		NodeID:            m.NodeID,
		UtilityID:         m.UtilityID,
		DesiredHash:       m.DesiredHash,
		DesiredSourceHash: m.DesiredSourceHash,
		AppliedHash:       m.AppliedHash,
		LastError:         m.LastError,
		DriftFailures:     m.DriftFailures,
		CreatedAt:         m.CreatedAt,
		UpdatedAt:         m.UpdatedAt,
	}
}
