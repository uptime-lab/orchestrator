package results

import (
	"time"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_adoption_job"
)

// NodeAdoptionJobStepResult is one step in the adoption progress checklist.
type NodeAdoptionJobStepResult struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// NodeAdoptionJobResult is the API/WebSocket representation of an adoption run.
type NodeAdoptionJobResult struct {
	ID                 string                      `json:"id"`
	AccountID          string                      `json:"account_id"`
	NodeID             string                      `json:"node_id,omitempty"`
	Host               string                      `json:"host"`
	Username           string                      `json:"username"`
	Status             string                      `json:"status"`
	Steps              []NodeAdoptionJobStepResult `json:"steps"`
	HostKeyFingerprint string                      `json:"host_key_fingerprint,omitempty"`
	Error              string                      `json:"error,omitempty"`
	CreatedAt          time.Time                       `json:"created_at"`
	UpdatedAt          time.Time                       `json:"updated_at"`
}

// NewNodeAdoptionJobResult maps a domain job to its API result.
func NewNodeAdoptionJobResult(j *node_adoption_job.Job) NodeAdoptionJobResult {
	steps := make([]NodeAdoptionJobStepResult, 0, len(j.Steps))
	for _, st := range j.Steps {
		steps = append(steps, NodeAdoptionJobStepResult{
			Key:     st.Key,
			Label:   st.Label,
			Status:  string(st.Status),
			Message: st.Message,
		})
	}
	res := NodeAdoptionJobResult{
		ID:                 j.ID.String(),
		AccountID:          j.AccountID.String(),
		Host:               j.Host,
		Username:           j.Username,
		Status:             string(j.Status),
		Steps:              steps,
		HostKeyFingerprint: j.HostKeyFingerprint,
		Error:              j.Error,
		CreatedAt:          j.CreatedAt,
		UpdatedAt:          j.UpdatedAt,
	}
	if j.NodeID != nil {
		res.NodeID = j.NodeID.String()
	}
	return res
}
