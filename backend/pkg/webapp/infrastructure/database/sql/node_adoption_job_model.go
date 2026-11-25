package sql

import (
	"encoding/json"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_adoption_job"
	"github.com/google/uuid"
)

// NodeAdoptionJobModel is the SQL representation of an SSH adoption run.
type NodeAdoptionJobModel struct {
	ID                 uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID          uuid.UUID  `gorm:"column:account_id;type:uuid;index;not null"`
	NodeID             *uuid.UUID `gorm:"column:node_id;type:uuid;index"`
	Host               string     `gorm:"column:host;not null"`
	Username           string     `gorm:"column:username;not null"`
	Status             string     `gorm:"column:status;size:20;not null"`
	Steps              string     `gorm:"column:steps;type:jsonb"`
	HostKeyFingerprint string     `gorm:"column:host_key_fingerprint;size:200"`
	Error              string     `gorm:"column:error;type:text"`
	CreatedAt          time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt          time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for this model.
func (NodeAdoptionJobModel) TableName() string { return "node_adoption_jobs" }

// ToDomain converts the SQL model to a domain job.
func (m *NodeAdoptionJobModel) ToDomain() *node_adoption_job.Job {
	var steps []node_adoption_job.Step
	if m.Steps != "" {
		_ = json.Unmarshal([]byte(m.Steps), &steps)
	}
	return &node_adoption_job.Job{
		ID:                 m.ID,
		AccountID:          m.AccountID,
		NodeID:             m.NodeID,
		Host:               m.Host,
		Username:           m.Username,
		Status:             node_adoption_job.JobStatus(m.Status),
		Steps:              steps,
		HostKeyFingerprint: m.HostKeyFingerprint,
		Error:              m.Error,
		CreatedAt:          m.CreatedAt,
		UpdatedAt:          m.UpdatedAt,
	}
}

// FromDomain updates the SQL model from a domain job.
func (m *NodeAdoptionJobModel) FromDomain(j *node_adoption_job.Job) {
	m.ID = j.ID
	m.AccountID = j.AccountID
	m.NodeID = j.NodeID
	m.Host = j.Host
	m.Username = j.Username
	m.Status = string(j.Status)
	if b, err := json.Marshal(j.Steps); err == nil {
		m.Steps = string(b)
	}
	m.HostKeyFingerprint = j.HostKeyFingerprint
	m.Error = j.Error
	m.CreatedAt = j.CreatedAt
	m.UpdatedAt = j.UpdatedAt
}
